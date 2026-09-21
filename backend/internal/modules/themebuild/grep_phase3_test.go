package themebuild

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/themefs"
)

// gatedThemeStore blocks every ReadFile until release is closed, while
// tracking how many reads are in flight — used to prove grep respects
// loadThemeFilesConcurrency without relying on timing races.
type gatedThemeStore struct {
	files   map[string]string
	tree    []themefs.FileTreeEntry
	release chan struct{}
	entered chan struct{} // closed once peak hit the expected concurrency

	inFlight atomic.Int32
	maxSeen  atomic.Int32
	once     sync.Once
}

func (s *gatedThemeStore) ListFiles(context.Context, themefs.RequestAuth) ([]themefs.FileTreeEntry, error) {
	return s.tree, nil
}

func (s *gatedThemeStore) ReadFile(ctx context.Context, _ themefs.RequestAuth, relPath string) (string, error) {
	cur := s.inFlight.Add(1)
	for {
		old := s.maxSeen.Load()
		if cur <= old || s.maxSeen.CompareAndSwap(old, cur) {
			break
		}
	}
	if int(cur) >= loadThemeFilesConcurrency {
		s.once.Do(func() { close(s.entered) })
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		s.inFlight.Add(-1)
		return "", ctx.Err()
	}
	s.inFlight.Add(-1)
	return s.files[relPath], nil
}

func (s *gatedThemeStore) WriteFile(context.Context, themefs.RequestAuth, string, string, *themefs.PageMeta) error {
	return themefs.ErrOverlayIsReadOnly
}

func (s *gatedThemeStore) DeleteFile(context.Context, themefs.RequestAuth, string) error {
	return themefs.ErrOverlayIsReadOnly
}

func TestExecGrepTheme_RespectsMaxConcurrency(t *testing.T) {
	t.Parallel()
	const nFiles = 32
	files := make(map[string]string, nFiles)
	tree := make([]themefs.FileTreeEntry, 0, nFiles)
	for i := 0; i < nFiles; i++ {
		p := fmt.Sprintf("pages/p%02d.liquid", i)
		files[p] = "needle here"
		tree = append(tree, themefs.FileTreeEntry{Name: p, Path: p, Type: "file"})
	}
	store := &gatedThemeStore{
		files:   files,
		tree:    tree,
		release: make(chan struct{}),
		entered: make(chan struct{}),
	}
	svc := &Service{}
	input, _ := json.Marshal(grepThemeInput{Pattern: "needle"})

	done := make(chan struct{})
	var out string
	var err error
	go func() {
		defer close(done)
		out, err = svc.execGrepTheme(context.Background(), store, testStoreAuth(), input, &ai.TurnMetrics{})
	}()

	select {
	case <-store.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for workers to reach concurrency cap")
	}
	if got := store.maxSeen.Load(); got > int32(loadThemeFilesConcurrency) {
		close(store.release)
		<-done
		t.Fatalf("max in-flight reads %d > loadThemeFilesConcurrency %d", got, loadThemeFilesConcurrency)
	}
	if got := store.inFlight.Load(); got > int32(loadThemeFilesConcurrency) {
		close(store.release)
		<-done
		t.Fatalf("in-flight %d exceeded cap %d", got, loadThemeFilesConcurrency)
	}
	close(store.release)
	<-done
	if err != nil {
		t.Fatalf("grep error: %v", err)
	}
	if !strings.Contains(out, "pages/p00.liquid:") {
		t.Fatalf("expected ordered matches, got %q", out[:min(80, len(out))])
	}
	if got := store.maxSeen.Load(); got != int32(loadThemeFilesConcurrency) {
		t.Fatalf("expected peak concurrency %d, got %d", loadThemeFilesConcurrency, got)
	}
}

func TestExecGrepTheme_DeterministicOrderUnderParallelism(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"pages/c.liquid": "MATCH c",
		"pages/a.liquid": "MATCH a",
		"pages/b.liquid": "MATCH b",
	}
	ts := newFakeThemeServer(t, files)
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}
	input, _ := json.Marshal(grepThemeInput{Pattern: "MATCH"})
	out, err := svc.execGrepTheme(context.Background(), svc.store, testStoreAuth(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := strings.Index(out, "pages/a.liquid:")
	b := strings.Index(out, "pages/b.liquid:")
	c := strings.Index(out, "pages/c.liquid:")
	if a < 0 || b < 0 || c < 0 || !(a < b && b < c) {
		t.Fatalf("expected sorted path order a<b<c, got:\n%s", out)
	}
}

func TestExecGrepTheme_NoMatches(t *testing.T) {
	t.Parallel()
	ts := newFakeThemeServer(t, map[string]string{"pages/home.liquid": "nothing relevant"})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}
	input, _ := json.Marshal(grepThemeInput{Pattern: "zzznomatch"})
	out, err := svc.execGrepTheme(context.Background(), svc.store, testStoreAuth(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "(no matches)" {
		t.Fatalf("got %q", out)
	}
}

func TestExecGrepTheme_SkipsUnreadableFile(t *testing.T) {
	t.Parallel()
	base := &failOnePathStore{
		ok: map[string]string{
			"pages/a.liquid": "MATCH ok",
			"pages/b.liquid": "MATCH fail-me",
		},
		failPath: "pages/b.liquid",
	}
	svc := &Service{}
	input, _ := json.Marshal(grepThemeInput{Pattern: "MATCH"})
	out, err := svc.execGrepTheme(context.Background(), base, testStoreAuth(), input, nil)
	if err != nil {
		t.Fatalf("grep should skip read errors, got %v", err)
	}
	if !strings.Contains(out, "pages/a.liquid:") {
		t.Fatalf("expected match from readable file, got %q", out)
	}
	if strings.Contains(out, "pages/b.liquid:") {
		t.Fatalf("failed file should be skipped, got %q", out)
	}
}

type failOnePathStore struct {
	ok       map[string]string
	failPath string
}

func (s *failOnePathStore) ListFiles(context.Context, themefs.RequestAuth) ([]themefs.FileTreeEntry, error) {
	tree := make([]themefs.FileTreeEntry, 0, len(s.ok))
	for p := range s.ok {
		tree = append(tree, themefs.FileTreeEntry{Name: p, Path: p, Type: "file"})
	}
	return tree, nil
}

func (s *failOnePathStore) ReadFile(_ context.Context, _ themefs.RequestAuth, relPath string) (string, error) {
	if relPath == s.failPath {
		return "", fmt.Errorf("simulated read failure")
	}
	return s.ok[relPath], nil
}

func (s *failOnePathStore) WriteFile(context.Context, themefs.RequestAuth, string, string, *themefs.PageMeta) error {
	return themefs.ErrOverlayIsReadOnly
}

func (s *failOnePathStore) DeleteFile(context.Context, themefs.RequestAuth, string) error {
	return themefs.ErrOverlayIsReadOnly
}

func TestExecGrepTheme_ContextCancelStopsWorkers(t *testing.T) {
	t.Parallel()
	const nFiles = 20
	files := make(map[string]string, nFiles)
	tree := make([]themefs.FileTreeEntry, 0, nFiles)
	for i := 0; i < nFiles; i++ {
		p := fmt.Sprintf("pages/p%02d.liquid", i)
		files[p] = "needle"
		tree = append(tree, themefs.FileTreeEntry{Name: p, Path: p, Type: "file"})
	}
	store := &gatedThemeStore{
		files:   files,
		tree:    tree,
		release: make(chan struct{}), // never released — only cancel unblocks
		entered: make(chan struct{}),
	}
	svc := &Service{}
	input, _ := json.Marshal(grepThemeInput{Pattern: "needle"})
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = svc.execGrepTheme(ctx, store, testStoreAuth(), input, nil)
	}()

	select {
	case <-store.entered:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("timed out waiting for workers")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		close(store.release)
		t.Fatal("grep did not return after context cancel")
	}
	// Workers must have unwound (in-flight back to 0) without release.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if store.inFlight.Load() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("workers still in-flight after cancel: %d", store.inFlight.Load())
}

func TestExecGrepTheme_CacheHitAvoidsBaseReads(t *testing.T) {
	t.Parallel()
	base := &countingGrepStore{
		files: map[string]string{
			"pages/home.liquid": "FINDME in home",
			"pages/about.liquid": "FINDME in about",
		},
	}
	for p := range base.files {
		base.tree = append(base.tree, themefs.FileTreeEntry{Name: p, Path: p, Type: "file"})
	}
	cache := themefs.NewCachingStore(base, themefs.ThemeKey{TenantID: 1, ThemeSlug: "demo"})
	store := themefs.NewOverlayStore(cache, nil)
	auth := testStoreAuth()
	svc := &Service{}
	input, _ := json.Marshal(grepThemeInput{Pattern: "FINDME"})
	metrics := &ai.TurnMetrics{}

	// Prime list + file cache via first grep.
	if _, err := svc.execGrepTheme(context.Background(), store, auth, input, metrics); err != nil {
		t.Fatal(err)
	}
	readsAfterFirst := base.reads.Load()
	listsAfterFirst := base.lists.Load()
	if readsAfterFirst < 2 || listsAfterFirst < 1 {
		t.Fatalf("expected priming reads, got reads=%d lists=%d", readsAfterFirst, listsAfterFirst)
	}

	metrics2 := &ai.TurnMetrics{}
	out, err := svc.execGrepTheme(context.Background(), store, auth, input, metrics2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "pages/about.liquid:") || !strings.Contains(out, "pages/home.liquid:") {
		t.Fatalf("unexpected output: %s", out)
	}
	if base.reads.Load() != readsAfterFirst {
		t.Fatalf("second grep should be cache-only reads; before=%d after=%d", readsAfterFirst, base.reads.Load())
	}
	if base.lists.Load() != listsAfterFirst {
		t.Fatalf("second grep should reuse cached ListFiles; before=%d after=%d", listsAfterFirst, base.lists.Load())
	}
	snap := metrics2.Snapshot()
	if snap.GrepCacheHits < 1 {
		t.Fatalf("expected cache hits on second grep, got %+v", snap)
	}
}

type countingGrepStore struct {
	files map[string]string
	tree  []themefs.FileTreeEntry
	reads atomic.Int64
	lists atomic.Int64
}

func (s *countingGrepStore) ListFiles(context.Context, themefs.RequestAuth) ([]themefs.FileTreeEntry, error) {
	s.lists.Add(1)
	return s.tree, nil
}

func (s *countingGrepStore) ReadFile(_ context.Context, _ themefs.RequestAuth, relPath string) (string, error) {
	s.reads.Add(1)
	return s.files[relPath], nil
}

func (s *countingGrepStore) WriteFile(context.Context, themefs.RequestAuth, string, string, *themefs.PageMeta) error {
	return themefs.ErrOverlayIsReadOnly
}

func (s *countingGrepStore) DeleteFile(context.Context, themefs.RequestAuth, string) error {
	return themefs.ErrOverlayIsReadOnly
}

func TestExecGrepTheme_MetricsIncludeConcurrency(t *testing.T) {
	t.Parallel()
	ts := newFakeThemeServer(t, map[string]string{
		"pages/a.liquid": "x",
		"pages/b.liquid": "x",
		"pages/c.liquid": "x",
	})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}
	input, _ := json.Marshal(grepThemeInput{Pattern: "x"})
	m := &ai.TurnMetrics{}
	if _, err := svc.execGrepTheme(context.Background(), svc.store, testStoreAuth(), input, m); err != nil {
		t.Fatal(err)
	}
	snap := m.Snapshot()
	if snap.GrepMaxConcurrency <= 0 || snap.GrepMaxConcurrency > loadThemeFilesConcurrency {
		t.Fatalf("max concurrency: %d", snap.GrepMaxConcurrency)
	}
	if snap.GrepFilesScanned != 3 || snap.GrepMatchesFound != 3 {
		t.Fatalf("snap=%+v", snap)
	}
}

func TestAsCachingStore_OverlayWrap(t *testing.T) {
	t.Parallel()
	base := &countingGrepStore{files: map[string]string{}}
	cache := themefs.NewCachingStore(base, themefs.ThemeKey{TenantID: 9, ThemeSlug: "s"})
	overlay := themefs.NewOverlayStore(cache, nil)
	if themefs.AsCachingStore(overlay) != cache {
		t.Fatal("expected AsCachingStore to unwrap overlay")
	}
	if themefs.AsCachingStore(cache) != cache {
		t.Fatal("expected direct CachingStore")
	}
	if themefs.AsCachingStore(base) != nil {
		t.Fatal("plain store should not report a cache")
	}
}

package themefs

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type countingStore struct {
	ThemeStore
	reads atomic.Int64
	lists atomic.Int64
	files map[string]string
	tree  []FileTreeEntry
}

func (s *countingStore) ReadFile(_ context.Context, _ RequestAuth, relPath string) (string, error) {
	s.reads.Add(1)
	return s.files[relPath], nil
}

func (s *countingStore) ListFiles(context.Context, RequestAuth) ([]FileTreeEntry, error) {
	s.lists.Add(1)
	return s.tree, nil
}

func (s *countingStore) WriteFile(context.Context, RequestAuth, string, string, *PageMeta) error {
	return ErrOverlayIsReadOnly
}

func (s *countingStore) DeleteFile(context.Context, RequestAuth, string) error {
	return ErrOverlayIsReadOnly
}

func testThemeKey(tenant uint64, slug string) ThemeKey {
	return ThemeKey{TenantID: tenant, ThemeSlug: slug}
}

func TestCachingStore_ReadFileHitsCache(t *testing.T) {
	t.Parallel()
	base := &countingStore{
		files: map[string]string{"pages/home.liquid": "hello"},
		tree:  []FileTreeEntry{{Name: "home.liquid", Path: "pages/home.liquid", Type: "file"}},
	}
	c := NewCachingStore(base, testThemeKey(1, "demo"))
	auth := RequestAuth{Token: "t", TenantID: 1}

	got, err := c.ReadFile(context.Background(), auth, "pages/home.liquid")
	if err != nil || got != "hello" {
		t.Fatalf("first read: got %q err %v", got, err)
	}
	got, err = c.ReadFile(context.Background(), auth, "pages/home.liquid")
	if err != nil || got != "hello" {
		t.Fatalf("second read: got %q err %v", got, err)
	}
	if base.reads.Load() != 1 {
		t.Fatalf("expected 1 underlying read, got %d", base.reads.Load())
	}
	st := c.Stats()
	if st.Hits != 1 || st.Misses != 1 || st.ReadHits != 1 || st.ReadMisses != 1 {
		t.Fatalf("stats=%+v, want hits/misses/readHits/readMisses 1", st)
	}
}

func TestCachingStore_ListFilesHitsCache(t *testing.T) {
	t.Parallel()
	base := &countingStore{
		tree: []FileTreeEntry{{Name: "a", Path: "a", Type: "file"}},
	}
	c := NewCachingStore(base, testThemeKey(1, "demo"))
	auth := RequestAuth{Token: "t", TenantID: 1}

	if _, err := c.ListFiles(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListFiles(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	if base.lists.Load() != 1 {
		t.Fatalf("expected 1 ListFiles, got %d", base.lists.Load())
	}
	st := c.Stats()
	if st.ListHits != 1 || st.ListMisses != 1 {
		t.Fatalf("stats listHits=%d listMisses=%d", st.ListHits, st.ListMisses)
	}
}

func TestCachingStore_DifferentFilesDoNotCollide(t *testing.T) {
	t.Parallel()
	base := &countingStore{
		files: map[string]string{
			"pages/a.liquid": "A",
			"pages/b.liquid": "B",
		},
	}
	c := NewCachingStore(base, testThemeKey(1, "demo"))
	auth := RequestAuth{Token: "t", TenantID: 1}

	a, err := c.ReadFile(context.Background(), auth, "pages/a.liquid")
	if err != nil || a != "A" {
		t.Fatalf("a: %q %v", a, err)
	}
	b, err := c.ReadFile(context.Background(), auth, "pages/b.liquid")
	if err != nil || b != "B" {
		t.Fatalf("b: %q %v", b, err)
	}
	if base.reads.Load() != 2 {
		t.Fatalf("expected 2 reads, got %d", base.reads.Load())
	}
}

func TestCachingStore_DifferentThemesDoNotCollide(t *testing.T) {
	t.Parallel()
	base := &countingStore{files: map[string]string{"pages/home.liquid": "from-base"}}
	auth := RequestAuth{Token: "t", TenantID: 1}

	cacheA := NewCachingStore(base, testThemeKey(1, "theme-a"))
	cacheB := NewCachingStore(base, testThemeKey(1, "theme-b"))
	cacheA.Put("pages/home.liquid", "content-a")
	cacheB.Put("pages/home.liquid", "content-b")

	gotA, err := cacheA.ReadFile(context.Background(), auth, "pages/home.liquid")
	if err != nil || gotA != "content-a" {
		t.Fatalf("theme-a: %q %v", gotA, err)
	}
	gotB, err := cacheB.ReadFile(context.Background(), auth, "pages/home.liquid")
	if err != nil || gotB != "content-b" {
		t.Fatalf("theme-b: %q %v", gotB, err)
	}
	if base.reads.Load() != 0 {
		t.Fatalf("Put-seeded caches should not hit base, got %d", base.reads.Load())
	}
}

func TestCachingStore_DifferentTenantsDoNotCollide(t *testing.T) {
	t.Parallel()
	base := &countingStore{files: map[string]string{"pages/home.liquid": "from-base"}}

	cache1 := NewCachingStore(base, testThemeKey(1, "shared-slug"))
	cache2 := NewCachingStore(base, testThemeKey(2, "shared-slug"))
	cache1.Put("pages/home.liquid", "tenant-1")
	cache2.Put("pages/home.liquid", "tenant-2")

	got1, err := cache1.ReadFile(context.Background(), RequestAuth{TenantID: 1}, "pages/home.liquid")
	if err != nil || got1 != "tenant-1" {
		t.Fatalf("tenant1: %q %v", got1, err)
	}
	got2, err := cache2.ReadFile(context.Background(), RequestAuth{TenantID: 2}, "pages/home.liquid")
	if err != nil || got2 != "tenant-2" {
		t.Fatalf("tenant2: %q %v", got2, err)
	}

	_, err = cache1.ReadFile(context.Background(), RequestAuth{TenantID: 2}, "pages/home.liquid")
	if err == nil || !strings.Contains(err.Error(), "tenant mismatch") {
		t.Fatalf("expected tenant mismatch, got %v", err)
	}
}

func TestCachingStore_GenerationIsolation(t *testing.T) {
	t.Parallel()
	base := &countingStore{files: map[string]string{"pages/home.liquid": "v1"}}
	auth := RequestAuth{Token: "t", TenantID: 1}
	key := testThemeKey(1, "demo")

	genA := NewCachingStore(base, key)
	got, err := genA.ReadFile(context.Background(), auth, "pages/home.liquid")
	if err != nil || got != "v1" {
		t.Fatalf("genA first: %q %v", got, err)
	}

	base.files["pages/home.liquid"] = "v2"
	genB := NewCachingStore(base, key)
	got, err = genB.ReadFile(context.Background(), auth, "pages/home.liquid")
	if err != nil || got != "v2" {
		t.Fatalf("genB must not see genA cache: %q %v", got, err)
	}
	got, err = genA.ReadFile(context.Background(), auth, "pages/home.liquid")
	if err != nil || got != "v1" {
		t.Fatalf("genA must keep its own cache: %q %v", got, err)
	}
}

func TestCachingStore_InvalidateForcesRefetch(t *testing.T) {
	t.Parallel()
	base := &countingStore{
		files: map[string]string{"pages/home.liquid": "v1"},
	}
	c := NewCachingStore(base, testThemeKey(1, "demo"))
	auth := RequestAuth{Token: "t", TenantID: 1}

	_, _ = c.ReadFile(context.Background(), auth, "pages/home.liquid")
	c.Invalidate("pages/home.liquid")
	base.files["pages/home.liquid"] = "v2"
	got, err := c.ReadFile(context.Background(), auth, "pages/home.liquid")
	if err != nil || got != "v2" {
		t.Fatalf("got %q err %v", got, err)
	}
	if base.reads.Load() != 2 {
		t.Fatalf("expected 2 reads after invalidate, got %d", base.reads.Load())
	}
}

func TestCachingStore_PutSeedsWithoutBaseRead(t *testing.T) {
	t.Parallel()
	base := &countingStore{files: map[string]string{}}
	c := NewCachingStore(base, testThemeKey(1, "demo"))
	c.Put("pages/home.liquid", "seeded")
	got, err := c.ReadFile(context.Background(), RequestAuth{TenantID: 1}, "pages/home.liquid")
	if err != nil || got != "seeded" {
		t.Fatalf("got %q err %v", got, err)
	}
	if base.reads.Load() != 0 {
		t.Fatalf("Put should avoid base read, got %d", base.reads.Load())
	}
}

func TestCachingStore_SingleflightCoalescesConcurrentReads(t *testing.T) {
	t.Parallel()
	base := &countingStore{
		files: map[string]string{"pages/home.liquid": "hello"},
	}
	c := NewCachingStore(base, testThemeKey(1, "demo"))
	auth := RequestAuth{Token: "t", TenantID: 1}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.ReadFile(context.Background(), auth, "pages/home.liquid")
		}()
	}
	wg.Wait()
	if base.reads.Load() != 1 {
		t.Fatalf("expected singleflight to coalesce to 1 base read, got %d", base.reads.Load())
	}
}

func TestCachingStore_MaxEntriesBound(t *testing.T) {
	t.Parallel()
	base := &countingStore{files: map[string]string{}}
	c := NewCachingStore(base, testThemeKey(1, "demo"))

	for i := 0; i < cachingStoreMaxEntries+25; i++ {
		c.Put("pages/file-"+itoa(i)+".liquid", "x")
	}
	st := c.Stats()
	if st.Entries > cachingStoreMaxEntries {
		t.Fatalf("entries=%d over max=%d", st.Entries, cachingStoreMaxEntries)
	}
	if len(c.files) > cachingStoreMaxEntries {
		t.Fatalf("files map len=%d over max", len(c.files))
	}
}

func TestCachingStore_MaxBytesBound(t *testing.T) {
	t.Parallel()
	base := &countingStore{files: map[string]string{}}
	c := NewCachingStore(base, testThemeKey(1, "demo"))

	chunk := strings.Repeat("z", 50_000) // 50KB each
	for i := 0; i < 1000; i++ {
		c.Put("pages/file-"+itoa(i)+".liquid", chunk)
	}
	st := c.Stats()
	if st.Bytes > cachingStoreMaxBytes {
		t.Fatalf("bytes=%d over max=%d", st.Bytes, cachingStoreMaxBytes)
	}
	if len(c.files) > cachingStoreMaxEntries {
		t.Fatalf("files map len=%d over max", len(c.files))
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [16]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

func TestCachingStore_OverlayDraftWinsOverCachedBase(t *testing.T) {
	t.Parallel()
	// Required stack: Overlay → Cache → base. Draft must win even if the
	// underlying path was previously cached from a base read.
	base := &countingStore{
		files: map[string]string{"pages/home.liquid": "original"},
	}
	cache := NewCachingStore(base, testThemeKey(1, "demo"))
	auth := RequestAuth{Token: "t", TenantID: 1}

	got, err := cache.ReadFile(context.Background(), auth, "pages/home.liquid")
	if err != nil || got != "original" {
		t.Fatalf("prime cache: %q %v", got, err)
	}
	if base.reads.Load() != 1 {
		t.Fatalf("expected 1 base read, got %d", base.reads.Load())
	}

	overlay := NewOverlayStore(cache, map[string]string{"pages/home.liquid": "draft-staged"})
	got, err = overlay.ReadFile(context.Background(), auth, "pages/home.liquid")
	if err != nil || got != "draft-staged" {
		t.Fatalf("overlay must return draft, got %q err %v", got, err)
	}
	// Overlay hit must not cause another base read.
	if base.reads.Load() != 1 {
		t.Fatalf("draft hit should not re-read base, got %d", base.reads.Load())
	}

	// Unrelated path still reuses the generation cache.
	base.files["pages/about.liquid"] = "about-body"
	got, err = overlay.ReadFile(context.Background(), auth, "pages/about.liquid")
	if err != nil || got != "about-body" {
		t.Fatalf("about: %q %v", got, err)
	}
	got, err = overlay.ReadFile(context.Background(), auth, "pages/about.liquid")
	if err != nil || got != "about-body" {
		t.Fatalf("about second: %q %v", got, err)
	}
	if base.reads.Load() != 2 {
		t.Fatalf("about should be 1 cached base read (total 2), got %d", base.reads.Load())
	}
}

func TestCachingStore_TableDrivenIsolation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		keyA  ThemeKey
		keyB  ThemeKey
		authA RequestAuth
		authB RequestAuth
	}{
		{
			name:  "same tenant different theme",
			keyA:  testThemeKey(1, "alpha"),
			keyB:  testThemeKey(1, "beta"),
			authA: RequestAuth{TenantID: 1},
			authB: RequestAuth{TenantID: 1},
		},
		{
			name:  "different tenant same theme slug",
			keyA:  testThemeKey(10, "shop"),
			keyB:  testThemeKey(20, "shop"),
			authA: RequestAuth{TenantID: 10},
			authB: RequestAuth{TenantID: 20},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := &countingStore{files: map[string]string{"x": "base"}}
			a := NewCachingStore(base, tt.keyA)
			b := NewCachingStore(base, tt.keyB)
			a.Put("x", "A")
			b.Put("x", "B")
			gotA, err := a.ReadFile(context.Background(), tt.authA, "x")
			if err != nil || gotA != "A" {
				t.Fatalf("A: %q %v", gotA, err)
			}
			gotB, err := b.ReadFile(context.Background(), tt.authB, "x")
			if err != nil || gotB != "B" {
				t.Fatalf("B: %q %v", gotB, err)
			}
		})
	}
}

package themefs

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// countingStore is a ThemeStore that counts how many times base methods are
// actually invoked, so tests can assert on cache hits/misses rather than
// just on returned values.
type countingStore struct {
	mu    sync.Mutex
	files map[string]string
	tree  []FileTreeEntry

	readCalls   map[string]int
	listCalls   int32
	writeCalls  int32
	deleteCalls int32

	// block, when non-nil, is closed by the test to release a ReadFile call
	// that's parked waiting for it — used to force two concurrent ReadFile
	// calls for the same path to overlap in time, so singleflight coalescing
	// actually has something to coalesce.
	block <-chan struct{}
}

func newCountingStore() *countingStore {
	return &countingStore{files: make(map[string]string), readCalls: make(map[string]int)}
}

func (c *countingStore) ReadFile(ctx context.Context, _ RequestAuth, relPath string) (string, error) {
	if c.block != nil {
		<-c.block
	}
	c.mu.Lock()
	c.readCalls[relPath]++
	content, ok := c.files[relPath]
	c.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("no fixture content for %q", relPath)
	}
	return content, nil
}

func (c *countingStore) WriteFile(context.Context, RequestAuth, string, string, *PageMeta) error {
	atomic.AddInt32(&c.writeCalls, 1)
	return nil
}

func (c *countingStore) DeleteFile(context.Context, RequestAuth, string) error {
	atomic.AddInt32(&c.deleteCalls, 1)
	return nil
}

func (c *countingStore) ListFiles(context.Context, RequestAuth) ([]FileTreeEntry, error) {
	atomic.AddInt32(&c.listCalls, 1)
	return c.tree, nil
}

func (c *countingStore) readCount(relPath string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readCalls[relPath]
}

// TestCachingStore_ReadFile_SecondReadIsCacheHit covers the whole point of
// CachingStore: a second ReadFile for the same path within one store's
// lifetime must not reach base again.
func TestCachingStore_ReadFile_SecondReadIsCacheHit(t *testing.T) {
	base := newCountingStore()
	base.files["pages.json"] = `{"routes":[]}`
	c := NewCachingStore(base)

	for i := 0; i < 3; i++ {
		got, err := c.ReadFile(context.Background(), RequestAuth{}, "pages.json")
		if err != nil {
			t.Fatalf("ReadFile %d failed: %v", i, err)
		}
		if got != `{"routes":[]}` {
			t.Fatalf("ReadFile %d returned %q", i, got)
		}
	}

	if got := base.readCount("pages.json"); got != 1 {
		t.Fatalf("expected exactly 1 base ReadFile call for a path read 3 times through the cache, got %d", got)
	}
}

// TestCachingStore_ReadFile_DifferentPathsAreIndependent guards against a
// cache keyed wrong (e.g. a single-entry cache instead of per-path) — two
// distinct paths must each be fetched from base exactly once.
func TestCachingStore_ReadFile_DifferentPathsAreIndependent(t *testing.T) {
	base := newCountingStore()
	base.files["pages.json"] = "A"
	base.files["defaults.json"] = "B"
	c := NewCachingStore(base)

	for i := 0; i < 2; i++ {
		if _, err := c.ReadFile(context.Background(), RequestAuth{}, "pages.json"); err != nil {
			t.Fatalf("ReadFile pages.json: %v", err)
		}
		if _, err := c.ReadFile(context.Background(), RequestAuth{}, "defaults.json"); err != nil {
			t.Fatalf("ReadFile defaults.json: %v", err)
		}
	}

	if got := base.readCount("pages.json"); got != 1 {
		t.Fatalf("pages.json: expected 1 base call, got %d", got)
	}
	if got := base.readCount("defaults.json"); got != 1 {
		t.Fatalf("defaults.json: expected 1 base call, got %d", got)
	}
}

// TestCachingStore_ReadFile_ConcurrentIdenticalReadsCoalesce covers the
// singleflight half of CachingStore's doc comment: two callers asking for
// the same not-yet-cached path at the same time must result in exactly one
// base ReadFile call, with both callers getting that one result.
func TestCachingStore_ReadFile_ConcurrentIdenticalReadsCoalesce(t *testing.T) {
	base := newCountingStore()
	base.files["pages/home.liquid"] = "HOME"
	block := make(chan struct{})
	base.block = block
	c := NewCachingStore(base)

	const n = 10
	results := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = c.ReadFile(context.Background(), RequestAuth{}, "pages/home.liquid")
		}(i)
	}

	close(block) // release every parked base.ReadFile call at once
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: ReadFile failed: %v", i, err)
		}
		if results[i] != "HOME" {
			t.Fatalf("goroutine %d: got %q, want %q", i, results[i], "HOME")
		}
	}
	if got := base.readCount("pages/home.liquid"); got != 1 {
		t.Fatalf("expected exactly 1 base ReadFile call across %d concurrent identical reads, got %d", n, got)
	}
}

// TestCachingStore_ListFiles_SecondCallIsCacheHit mirrors
// TestCachingStore_ReadFile_SecondReadIsCacheHit for the tree cache —
// buildThemeContext and buildSnapshotBase each call ListFiles independently
// within one generation today, and that duplication is exactly what this
// cache removes.
func TestCachingStore_ListFiles_SecondCallIsCacheHit(t *testing.T) {
	base := newCountingStore()
	base.tree = []FileTreeEntry{{Name: "pages.json", Path: "pages.json", Type: "file"}}
	c := NewCachingStore(base)

	for i := 0; i < 3; i++ {
		tree, err := c.ListFiles(context.Background(), RequestAuth{})
		if err != nil {
			t.Fatalf("ListFiles %d failed: %v", i, err)
		}
		if len(tree) != 1 {
			t.Fatalf("ListFiles %d returned %d entries, want 1", i, len(tree))
		}
	}

	if got := atomic.LoadInt32(&base.listCalls); got != 1 {
		t.Fatalf("expected exactly 1 base ListFiles call, got %d", got)
	}
}

// TestCachingStore_WriteFile_InvalidatesThatPathAndTree covers Invalidate:
// a write through the cache must drop that path's cached content (so a
// later ReadFile sees the new value, not a stale cached one) and drop the
// cached tree (a write can add a brand-new path the old tree didn't have).
func TestCachingStore_WriteFile_InvalidatesThatPathAndTree(t *testing.T) {
	base := newCountingStore()
	base.files["pages/home.liquid"] = "OLD"
	base.tree = []FileTreeEntry{{Name: "home.liquid", Path: "pages/home.liquid", Type: "file"}}
	c := NewCachingStore(base)

	if _, err := c.ReadFile(context.Background(), RequestAuth{}, "pages/home.liquid"); err != nil {
		t.Fatalf("initial ReadFile: %v", err)
	}
	if _, err := c.ListFiles(context.Background(), RequestAuth{}); err != nil {
		t.Fatalf("initial ListFiles: %v", err)
	}

	if err := c.WriteFile(context.Background(), RequestAuth{}, "pages/home.liquid", "NEW", nil); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	base.files["pages/home.liquid"] = "NEW"

	got, err := c.ReadFile(context.Background(), RequestAuth{}, "pages/home.liquid")
	if err != nil {
		t.Fatalf("post-write ReadFile: %v", err)
	}
	if got != "NEW" {
		t.Fatalf("expected the invalidated path to be re-fetched and return %q, got %q", "NEW", got)
	}
	if got := base.readCount("pages/home.liquid"); got != 2 {
		t.Fatalf("expected 2 base ReadFile calls (initial + post-invalidate), got %d", got)
	}

	if _, err := c.ListFiles(context.Background(), RequestAuth{}); err != nil {
		t.Fatalf("post-write ListFiles: %v", err)
	}
	if got := atomic.LoadInt32(&base.listCalls); got != 2 {
		t.Fatalf("expected the tree cache to be dropped by WriteFile, forcing a 2nd base ListFiles call, got %d", got)
	}
}

// TestCachingStore_DeleteFile_Invalidates mirrors the WriteFile case for
// DeleteFile.
func TestCachingStore_DeleteFile_Invalidates(t *testing.T) {
	base := newCountingStore()
	base.files["pages/old.liquid"] = "GONE SOON"
	c := NewCachingStore(base)

	if _, err := c.ReadFile(context.Background(), RequestAuth{}, "pages/old.liquid"); err != nil {
		t.Fatalf("initial ReadFile: %v", err)
	}
	if err := c.DeleteFile(context.Background(), RequestAuth{}, "pages/old.liquid"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}

	delete(base.files, "pages/old.liquid")
	if _, err := c.ReadFile(context.Background(), RequestAuth{}, "pages/old.liquid"); err == nil {
		t.Fatal("expected a re-fetch after delete to miss (no fixture content left), proving the cached entry was dropped")
	}
	if got := base.readCount("pages/old.liquid"); got != 2 {
		t.Fatalf("expected 2 base ReadFile calls (initial + post-invalidate re-fetch), got %d", got)
	}
}

// TestCachingStore_ReadFile_ErrorNotCached covers a correctness edge: a
// failed base read must not be cached as if it were a successful empty
// read, or a transient FlowPOS error would poison every later read of that
// path for the rest of the generation.
func TestCachingStore_ReadFile_ErrorNotCached(t *testing.T) {
	base := newCountingStore() // no fixture for "missing.liquid" -> ReadFile errors
	c := NewCachingStore(base)

	if _, err := c.ReadFile(context.Background(), RequestAuth{}, "missing.liquid"); err == nil {
		t.Fatal("expected the first read to fail (no fixture content)")
	}
	base.files["missing.liquid"] = "NOW EXISTS"
	got, err := c.ReadFile(context.Background(), RequestAuth{}, "missing.liquid")
	if err != nil {
		t.Fatalf("expected the second read to succeed now that base has content, got error: %v", err)
	}
	if got != "NOW EXISTS" {
		t.Fatalf("got %q, want %q", got, "NOW EXISTS")
	}
	if got := base.readCount("missing.liquid"); got != 2 {
		t.Fatalf("expected the errored first read to not be cached, forcing a 2nd base call, got %d base calls", got)
	}
}

// TestCachingStore_CacheFile_StopsCachingPastEntryCap covers
// cachingStoreMaxEntries: once the cache holds that many distinct paths, a
// new path is served correctly but not retained — proven by a 2nd read of
// that same new path costing a 2nd base call.
func TestCachingStore_CacheFile_StopsCachingPastEntryCap(t *testing.T) {
	base := newCountingStore()
	c := NewCachingStore(base)
	c.files = make(map[string]string, cachingStoreMaxEntries)
	for i := 0; i < cachingStoreMaxEntries; i++ {
		path := fmt.Sprintf("pages/filler-%d.liquid", i)
		c.files[path] = "x"
	}

	overflowPath := "pages/overflow.liquid"
	base.files[overflowPath] = "OVERFLOW"

	if _, err := c.ReadFile(context.Background(), RequestAuth{}, overflowPath); err != nil {
		t.Fatalf("first read of overflow path: %v", err)
	}
	if _, err := c.ReadFile(context.Background(), RequestAuth{}, overflowPath); err != nil {
		t.Fatalf("second read of overflow path: %v", err)
	}

	if got := base.readCount(overflowPath); got != 2 {
		t.Fatalf("expected a path read once the cache is at its entry cap to hit base every time (not retained), got %d base calls, want 2", got)
	}
	if len(c.files) != cachingStoreMaxEntries {
		t.Fatalf("expected the cache to stay at its cap of %d entries, got %d", cachingStoreMaxEntries, len(c.files))
	}
}

// TestCachingStore_SatisfiesThemeStore is a compile-time-ish guard: anything
// depending on themefs.ThemeStore (doGenerate's store var, buildToolExecutor,
// etc.) must be able to hold a *CachingStore.
func TestCachingStore_SatisfiesThemeStore(t *testing.T) {
	var _ ThemeStore = NewCachingStore(newCountingStore())
}

package themefs

import (
	"context"
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

func TestCachingStore_ReadFileHitsCache(t *testing.T) {
	base := &countingStore{
		files: map[string]string{"pages/home.liquid": "hello"},
		tree:  []FileTreeEntry{{Name: "home.liquid", Path: "pages/home.liquid", Type: "file"}},
	}
	c := NewCachingStore(base)
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
	hits, misses, _ := c.Stats()
	if hits != 1 || misses != 1 {
		t.Fatalf("stats hits=%d misses=%d, want 1/1", hits, misses)
	}
}

func TestCachingStore_ListFilesHitsCache(t *testing.T) {
	base := &countingStore{
		tree: []FileTreeEntry{{Name: "a", Path: "a", Type: "file"}},
	}
	c := NewCachingStore(base)
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
}

func TestCachingStore_InvalidateForcesRefetch(t *testing.T) {
	base := &countingStore{
		files: map[string]string{"pages/home.liquid": "v1"},
	}
	c := NewCachingStore(base)
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
	base := &countingStore{files: map[string]string{}}
	c := NewCachingStore(base)
	c.Put("pages/home.liquid", "seeded")
	got, err := c.ReadFile(context.Background(), RequestAuth{}, "pages/home.liquid")
	if err != nil || got != "seeded" {
		t.Fatalf("got %q err %v", got, err)
	}
	if base.reads.Load() != 0 {
		t.Fatalf("Put should avoid base read, got %d", base.reads.Load())
	}
}

func TestCachingStore_SingleflightCoalescesConcurrentReads(t *testing.T) {
	base := &countingStore{
		files: map[string]string{"pages/home.liquid": "hello"},
	}
	c := NewCachingStore(base)
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

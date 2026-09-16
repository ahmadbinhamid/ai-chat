package themefs

import (
	"context"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/singleflight"
)

// cachingStoreMaxEntries bounds how many ReadFile paths one CachingStore
// retains. A generation-scoped cache (see NewCachingStore) only lives for
// one doGenerate call; this cap is defense in depth so a pathological theme
// or a runaway tool loop cannot grow process memory without bound via the
// cache alone. Eviction is arbitrary (delete one map entry), not LRU: a
// wrongly-evicted entry only costs one extra FlowPOS round trip.
const cachingStoreMaxEntries = 2048

// CachingStore wraps a ThemeStore with a generation-scoped in-memory cache
// of ListFiles + ReadFile results. It is intended to wrap the draft
// OverlayStore for the life of one doGenerate call so buildThemeContext,
// buildSnapshotBase, read_theme_file, and grep_theme share FlowPOS work
// instead of re-fetching the same paths. Not safe to share across tenants
// or generations — construct a fresh one per call.
//
// Concurrent ReadFile/ListFiles for the same key coalesce via singleflight
// so parallel tools asking for the same path issue one FlowPOS request.
type CachingStore struct {
	base ThemeStore

	mu         sync.Mutex
	files      map[string]cachedFile
	tree       []FileTreeEntry
	treeCached bool

	hits        atomic.Int64
	misses      atomic.Int64
	invalidated atomic.Int64

	readGroup singleflight.Group
	listGroup singleflight.Group
}

type cachedFile struct {
	content string
	// present distinguishes "cached empty/missing file" (ReadFile's
	// ("", nil) not-found case) from "not in cache".
	present bool
}

// NewCachingStore returns a ThemeStore that caches ReadFile/ListFiles
// against base for the caller's lifetime.
func NewCachingStore(base ThemeStore) *CachingStore {
	return &CachingStore{
		base:  base,
		files: make(map[string]cachedFile),
	}
}

// Stats returns cache hit/miss/invalidation counters for structured logging.
func (c *CachingStore) Stats() (hits, misses, invalidated int64) {
	return c.hits.Load(), c.misses.Load(), c.invalidated.Load()
}

// Put stores content for relPath (including empty string for a known-empty
// or draft-deleted file). Used when the caller already holds authoritative
// bytes and wants subsequent ReadFile calls to reuse them.
func (c *CachingStore) Put(relPath, content string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.putLocked(relPath, content)
}

// Invalidate drops a cached path (and the file-tree cache, since a new path
// may appear or disappear). Next ReadFile/ListFiles refetches.
func (c *CachingStore) Invalidate(relPath string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.files, relPath)
	c.treeCached = false
	c.tree = nil
	c.invalidated.Add(1)
}

func (c *CachingStore) putLocked(relPath, content string) {
	if len(c.files) >= cachingStoreMaxEntries {
		for k := range c.files {
			delete(c.files, k)
			break
		}
	}
	c.files[relPath] = cachedFile{content: content, present: true}
}

func (c *CachingStore) ReadFile(ctx context.Context, auth RequestAuth, relPath string) (string, error) {
	c.mu.Lock()
	if cf, ok := c.files[relPath]; ok && cf.present {
		c.mu.Unlock()
		c.hits.Add(1)
		return cf.content, nil
	}
	c.mu.Unlock()

	// Path-only key is safe: one CachingStore serves one generation's auth.
	v, err, _ := c.readGroup.Do(relPath, func() (any, error) {
		c.mu.Lock()
		if cf, ok := c.files[relPath]; ok && cf.present {
			c.mu.Unlock()
			return cf.content, nil
		}
		c.mu.Unlock()

		c.misses.Add(1)
		content, err := c.base.ReadFile(ctx, auth, relPath)
		if err != nil {
			return "", err
		}
		c.mu.Lock()
		c.putLocked(relPath, content)
		c.mu.Unlock()
		return content, nil
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

func (c *CachingStore) WriteFile(ctx context.Context, auth RequestAuth, relPath, content string, meta *PageMeta) error {
	err := c.base.WriteFile(ctx, auth, relPath, content, meta)
	if err != nil {
		return err
	}
	c.Put(relPath, content)
	c.mu.Lock()
	c.treeCached = false
	c.tree = nil
	c.mu.Unlock()
	return nil
}

func (c *CachingStore) DeleteFile(ctx context.Context, auth RequestAuth, relPath string) error {
	err := c.base.DeleteFile(ctx, auth, relPath)
	if err != nil {
		return err
	}
	c.Invalidate(relPath)
	return nil
}

func (c *CachingStore) ListFiles(ctx context.Context, auth RequestAuth) ([]FileTreeEntry, error) {
	c.mu.Lock()
	if c.treeCached {
		tree := c.tree
		c.mu.Unlock()
		c.hits.Add(1)
		return tree, nil
	}
	c.mu.Unlock()

	v, err, _ := c.listGroup.Do("list", func() (any, error) {
		c.mu.Lock()
		if c.treeCached {
			tree := c.tree
			c.mu.Unlock()
			return tree, nil
		}
		c.mu.Unlock()

		c.misses.Add(1)
		tree, err := c.base.ListFiles(ctx, auth)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.tree = tree
		c.treeCached = true
		c.mu.Unlock()
		return tree, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]FileTreeEntry), nil
}

package themefs

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/singleflight"
)

// Bounds for one generation-scoped CachingStore. Aligned with themebuild's
// tool caps (grep scans ≤500 files; read_theme_file caps one call at 40KB)
// so a generation that greps broadly still fits, while a runaway loop cannot
// grow process memory without bound via the cache alone.
const (
	cachingStoreMaxEntries = 512
	cachingStoreMaxBytes   = 512 * 40_000 // 20 MiB
)

// ThemeKey identifies the tenant+theme a CachingStore may serve. Required at
// construction so cache identity is never theme_slug alone — even though the
// store itself is generation-scoped and not shared.
type ThemeKey struct {
	TenantID  uint64
	ThemeSlug string
}

// CachingStore wraps a ThemeStore with a generation-scoped in-memory cache
// of ListFiles + ReadFile results. Construct one per doGenerate call, typically
// under OverlayStore:
//
//	OverlayStore → CachingStore → FlowPOS / workspace
//
// so draft overrides stay authoritative and the cache only retains base reads.
// Not safe to share across tenants or generations.
//
// Concurrent ReadFile/ListFiles for the same key coalesce via singleflight
// so parallel tools asking for the same path issue one underlying request.
type CachingStore struct {
	base ThemeStore
	key  ThemeKey

	mu         sync.Mutex
	files      map[string]cachedFile
	bytes      int64
	tree       []FileTreeEntry
	treeCached bool

	hits            atomic.Int64
	misses          atomic.Int64
	listHits        atomic.Int64
	readHits        atomic.Int64
	listMisses      atomic.Int64
	readMisses      atomic.Int64
	invalidated     atomic.Int64
	skippedOversize atomic.Int64

	readGroup singleflight.Group
	listGroup singleflight.Group
}

type cachedFile struct {
	content string
	// present distinguishes "cached empty/missing file" (ReadFile's
	// ("", nil) not-found case) from "not in cache".
	present bool
}

// CacheStats is a point-in-time snapshot of CachingStore counters for logs.
type CacheStats struct {
	Hits            int64
	Misses          int64
	ListHits        int64
	ReadHits        int64
	ListMisses      int64
	ReadMisses      int64
	Invalidated     int64
	SkippedOversize int64
	Entries         int
	Bytes           int64
}

// NewCachingStore returns a ThemeStore that caches ReadFile/ListFiles against
// base for the caller's lifetime. key must identify the tenant+theme; every
// ReadFile/ListFiles call's auth.TenantID must match key.TenantID.
func NewCachingStore(base ThemeStore, key ThemeKey) *CachingStore {
	return &CachingStore{
		base:  base,
		key:   key,
		files: make(map[string]cachedFile),
	}
}

// Key returns the tenant+theme this cache was constructed for.
func (c *CachingStore) Key() ThemeKey { return c.key }

// Stats returns cache hit/miss/size counters for structured logging.
func (c *CachingStore) Stats() CacheStats {
	c.mu.Lock()
	entries := len(c.files)
	if c.treeCached {
		entries++ // count the list-tree as one logical entry
	}
	bytes := c.bytes
	c.mu.Unlock()
	return CacheStats{
		Hits:            c.hits.Load(),
		Misses:          c.misses.Load(),
		ListHits:        c.listHits.Load(),
		ReadHits:        c.readHits.Load(),
		ListMisses:      c.listMisses.Load(),
		ReadMisses:      c.readMisses.Load(),
		Invalidated:     c.invalidated.Load(),
		SkippedOversize: c.skippedOversize.Load(),
		Entries:         entries,
		Bytes:           bytes,
	}
}

func (c *CachingStore) checkTenant(auth RequestAuth) error {
	if auth.TenantID != c.key.TenantID {
		return fmt.Errorf("themefs: caching store tenant mismatch: auth=%d cache=%d theme=%q",
			auth.TenantID, c.key.TenantID, c.key.ThemeSlug)
	}
	return nil
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
	if cf, ok := c.files[relPath]; ok {
		c.bytes -= int64(len(cf.content))
		delete(c.files, relPath)
	}
	c.treeCached = false
	c.tree = nil
	c.invalidated.Add(1)
}

func (c *CachingStore) putLocked(relPath, content string) {
	contentBytes := int64(len(content))
	if contentBytes > cachingStoreMaxBytes {
		// Single file larger than the whole budget — do not retain.
		c.skippedOversize.Add(1)
		return
	}
	if existing, ok := c.files[relPath]; ok {
		c.bytes -= int64(len(existing.content))
		delete(c.files, relPath)
	}
	for (len(c.files) >= cachingStoreMaxEntries || c.bytes+contentBytes > cachingStoreMaxBytes) && len(c.files) > 0 {
		for k, cf := range c.files {
			c.bytes -= int64(len(cf.content))
			delete(c.files, k)
			break
		}
	}
	if c.bytes+contentBytes > cachingStoreMaxBytes {
		c.skippedOversize.Add(1)
		return
	}
	c.files[relPath] = cachedFile{content: content, present: true}
	c.bytes += contentBytes
}

func (c *CachingStore) ReadFile(ctx context.Context, auth RequestAuth, relPath string) (string, error) {
	if err := c.checkTenant(auth); err != nil {
		return "", err
	}

	c.mu.Lock()
	if cf, ok := c.files[relPath]; ok && cf.present {
		c.mu.Unlock()
		c.hits.Add(1)
		c.readHits.Add(1)
		return cf.content, nil
	}
	c.mu.Unlock()

	// Path-only singleflight key is safe: one CachingStore serves one
	// generation's ThemeKey (tenant+theme); tenant is checked above.
	v, err, _ := c.readGroup.Do(relPath, func() (any, error) {
		c.mu.Lock()
		if cf, ok := c.files[relPath]; ok && cf.present {
			c.mu.Unlock()
			return cf.content, nil
		}
		c.mu.Unlock()

		c.misses.Add(1)
		c.readMisses.Add(1)
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
	if err := c.checkTenant(auth); err != nil {
		return err
	}
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
	if err := c.checkTenant(auth); err != nil {
		return err
	}
	err := c.base.DeleteFile(ctx, auth, relPath)
	if err != nil {
		return err
	}
	c.Invalidate(relPath)
	return nil
}

func (c *CachingStore) ListFiles(ctx context.Context, auth RequestAuth) ([]FileTreeEntry, error) {
	if err := c.checkTenant(auth); err != nil {
		return nil, err
	}

	c.mu.Lock()
	if c.treeCached {
		tree := c.tree
		c.mu.Unlock()
		c.hits.Add(1)
		c.listHits.Add(1)
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
		c.listMisses.Add(1)
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

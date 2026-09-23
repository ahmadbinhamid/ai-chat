package themefs

import (
	"context"
	"sync"

	"golang.org/x/sync/singleflight"
)

// cachingStoreMaxEntries/cachingStoreMaxBytes bound memory: grep_theme alone can ReadFile up to 500 files per call,
// and the cap prevents a pathological generation from holding unbounded theme content for the call's lifetime.
const (
	cachingStoreMaxEntries = 512
	cachingStoreMaxBytes   = 20 * 1024 * 1024
)

// listFilesKey is namespaced with a leading NUL so it can never collide with a real relPath, which ReadFile keys on directly.
const listFilesKey = "\x00list_files"

// CachingStore wraps a ThemeStore with an in-memory read cache scoped to one generation call, coalescing concurrent reads via singleflight.
// Build exactly one per generation, never share across generations or chats — a theme can change between turns, so caching beyond its lifetime risks stale content.
type CachingStore struct {
	base ThemeStore

	mu        sync.Mutex
	files     map[string]string
	fileBytes int
	tree      []FileTreeEntry
	haveTree  bool

	sf singleflight.Group
}

// NewCachingStore wraps base with a per-generation read cache — callers must build a fresh one per generation, never reuse across calls.
func NewCachingStore(base ThemeStore) *CachingStore {
	return &CachingStore{base: base, files: make(map[string]string)}
}

// ReadFile serves relPath from cache, otherwise fetches via base, coalescing concurrent identical calls via singleflight.
// Checked both before and inside the singleflight call — a late arrival after an already-finished call needs the inner check to avoid a redundant fetch.
func (c *CachingStore) ReadFile(ctx context.Context, auth RequestAuth, relPath string) (string, error) {
	if content, ok := c.cachedFile(relPath); ok {
		return content, nil
	}

	v, err, _ := c.sf.Do(relPath, func() (any, error) {
		if content, ok := c.cachedFile(relPath); ok {
			return content, nil
		}
		content, err := c.base.ReadFile(ctx, auth, relPath)
		if err != nil {
			return "", err
		}
		c.cacheFile(relPath, content)
		return content, nil
	})
	if err != nil {
		return "", err
	}
	content, _ := v.(string)
	return content, nil
}

func (c *CachingStore) cachedFile(relPath string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	content, ok := c.files[relPath]
	return content, ok
}

// ListFiles caches the file tree as one shared value, not bounded by cachingStoreMaxEntries/cachingStoreMaxBytes since
// only one copy is ever held. Same double-check-around-singleflight reasoning as ReadFile.
func (c *CachingStore) ListFiles(ctx context.Context, auth RequestAuth) ([]FileTreeEntry, error) {
	if tree, ok := c.cachedTree(); ok {
		return tree, nil
	}

	v, err, _ := c.sf.Do(listFilesKey, func() (any, error) {
		if tree, ok := c.cachedTree(); ok {
			return tree, nil
		}
		tree, err := c.base.ListFiles(ctx, auth)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.tree, c.haveTree = tree, true
		c.mu.Unlock()
		return tree, nil
	})
	if err != nil {
		return nil, err
	}
	tree, _ := v.([]FileTreeEntry)
	return tree, nil
}

func (c *CachingStore) cachedTree() ([]FileTreeEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.haveTree {
		return nil, false
	}
	return c.tree, true
}

// WriteFile passes through to base, then drops relPath and the now-stale file tree from the cache.
func (c *CachingStore) WriteFile(ctx context.Context, auth RequestAuth, relPath, content string, meta *PageMeta) error {
	if err := c.base.WriteFile(ctx, auth, relPath, content, meta); err != nil {
		return err
	}
	c.invalidate(relPath)
	return nil
}

// DeleteFile passes through to base, then drops relPath and the now-stale file tree from the cache, same as WriteFile.
func (c *CachingStore) DeleteFile(ctx context.Context, auth RequestAuth, relPath string) error {
	if err := c.base.DeleteFile(ctx, auth, relPath); err != nil {
		return err
	}
	c.invalidate(relPath)
	return nil
}

func (c *CachingStore) cacheFile(relPath, content string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.files[relPath]; exists {
		return
	}
	if len(c.files) >= cachingStoreMaxEntries || c.fileBytes+len(content) > cachingStoreMaxBytes {
		// Full — stop caching rather than evicting; missing the cache on a new path costs one extra HTTP round trip, not a correctness problem.
		return
	}
	c.fileBytes += len(content)
	c.files[relPath] = content
}

func (c *CachingStore) invalidate(relPath string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if content, ok := c.files[relPath]; ok {
		c.fileBytes -= len(content)
		delete(c.files, relPath)
	}
	// A write/delete can change the tree, so drop it rather than trying to patch it in place.
	c.tree, c.haveTree = nil, false
}

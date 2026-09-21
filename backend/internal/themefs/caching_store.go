package themefs

import (
	"context"
	"sync"

	"golang.org/x/sync/singleflight"
)

// cachingStoreMaxEntries and cachingStoreMaxBytes bound how much theme
// content a CachingStore holds onto. grep_theme alone can ReadFile up to 500
// distinct files in one call (see tool_exec.go's maxGrepFilesScanned); the
// entry cap comfortably covers that without letting a pathological
// generation (many greps, many large files) hold an unbounded amount of
// theme content in memory for the life of one doGenerate call.
const (
	cachingStoreMaxEntries = 512
	cachingStoreMaxBytes   = 20 * 1024 * 1024
)

// listFilesKey is the singleflight/cache key ListFiles uses — namespaced
// away from any real relPath (which ReadFile keys on directly) with a
// leading NUL, since both "" and any real theme path are otherwise valid
// ReadFile keys and neither is safe to reuse here.
const listFilesKey = "\x00list_files"

// CachingStore wraps a ThemeStore with an in-memory read cache scoped to a
// single generation.
//
// It exists because one generation re-reads the same theme paths many times
// over with no sharing between the call sites doing it: grep_theme alone can
// ReadFile up to 500 files (tool_exec.go), and buildThemeContext,
// buildSnapshotBase, read_theme_file, and list_theme_files each
// independently re-fetch pages.json, defaults.json, and the file tree —
// every one of those reads goes over HTTP to flowpos-backend today with
// nothing shared between them.
//
// Scope and lifetime: build exactly one CachingStore per generation call,
// wrapping that generation's own OverlayStore, and let it go out of scope
// with the rest of that call's state — never store one on Service, never
// share one across generations or chats. A theme can change between turns
// (the merchant, or a concurrent generation on another chat, can write to
// it), so caching beyond one generation's lifetime would risk serving stale
// content. Within one generation, the only writer that could invalidate this
// cache's assumptions is this same wrapped store's own WriteFile/DeleteFile,
// which Invalidate (via WriteFile/DeleteFile below) handles — in practice
// nothing calls those through the generation-scoped store today, since
// writes only happen later via Service.ApplyDraft against the base store
// directly (see OverlayStore's own doc comment), so this exists for
// correctness if that ever changes, not because it fires on the hot path.
//
// Concurrent identical reads — e.g. buildThemeContext's own errgroup reading
// pages.json at the same moment a tool call also wants it — are coalesced
// via singleflight so only one of them actually reaches the network; the
// rest wait for and share that one result.
type CachingStore struct {
	base ThemeStore

	mu        sync.Mutex
	files     map[string]string
	fileBytes int
	tree      []FileTreeEntry
	haveTree  bool

	sf singleflight.Group
}

// NewCachingStore wraps base with a per-generation read cache. See
// CachingStore's own doc comment for scope and lifetime — callers should
// build a fresh one per generation, not reuse one across calls.
func NewCachingStore(base ThemeStore) *CachingStore {
	return &CachingStore{base: base, files: make(map[string]string)}
}

// ReadFile serves relPath from cache when present, otherwise fetches it
// through base — coalescing concurrent callers asking for the same path via
// singleflight — and caches the result for the rest of this store's life.
//
// The cache is checked both before and inside the singleflight call: a
// singleflight group only coalesces callers that are concurrently in
// flight, so a caller arriving just after an identical, already-cached call
// finished would otherwise trigger its own redundant base fetch — the
// inner check catches that case and returns the now-cached value instead.
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

// ListFiles serves the file tree from cache once it's been fetched once —
// the tree is a single value shared by every caller in this generation, not
// bounded by cachingStoreMaxEntries/cachingStoreMaxBytes, since exactly one
// copy is ever held regardless of how many times ListFiles is called. Same
// double-check-around-singleflight reasoning as ReadFile above.
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

// WriteFile passes through to base, then drops relPath (and the now-stale
// file tree) from the cache — see CachingStore's own doc comment on why
// nothing in the generation path is expected to reach this today.
func (c *CachingStore) WriteFile(ctx context.Context, auth RequestAuth, relPath, content string, meta *PageMeta) error {
	if err := c.base.WriteFile(ctx, auth, relPath, content, meta); err != nil {
		return err
	}
	c.invalidate(relPath)
	return nil
}

// DeleteFile passes through to base, then drops relPath (and the now-stale
// file tree) from the cache — same reasoning as WriteFile above.
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
		// Full — stop caching new entries rather than evicting an arbitrary
		// one. Within a single generation the same handful of hot paths
		// (pages.json, defaults.json, layout files, whatever page is
		// actually being worked on) get re-read; missing the cache on one
		// more distinct path costs a single extra HTTP round trip, not a
		// correctness problem, so there's nothing to gain from evicting an
		// already-cached, still-likely-useful entry to make room this late.
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
	// A write or delete can add or remove a path from the tree, so the
	// cached tree is no longer trustworthy — drop it rather than trying to
	// patch it in place.
	c.tree, c.haveTree = nil, false
}

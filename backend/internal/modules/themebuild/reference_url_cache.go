package themebuild

import (
	"fmt"
	"sync"
	"time"
)

// Balance: avoid refetch within turn, yet refresh stale pages promptly.
const referenceURLCacheTTL = 5 * time.Minute

// Caps by bytes (explicit memory bound), not entry count.
const referenceURLCacheMaxBytes = 64 * 1024 * 1024

// Caches digest content (skips re-fetch/rebuild on hit); title names the page.
type cachedReference struct {
	content   string
	title     string
	truncated bool
	expiresAt time.Time
}

func entryBytes(entry cachedReference) int {
	return len(entry.content) + len(entry.title)
}

// Keyed by tenant+URL (never bare URL: client-influenced, collides across tenants). Successes only.
type referenceURLCache struct {
	mu         sync.Mutex
	entries    map[string]cachedReference
	totalBytes int
}

func newReferenceURLCache() *referenceURLCache {
	return &referenceURLCache{entries: make(map[string]cachedReference)}
}

func referenceURLCacheKey(tenantID uint64, url string) string {
	return fmt.Sprintf("%d:%s", tenantID, url)
}

// Returns cached content if present/unexpired; prunes expired entries (no background sweep).
func (c *referenceURLCache) get(tenantID uint64, url string) (content, title string, truncated bool, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := referenceURLCacheKey(tenantID, url)
	entry, exists := c.entries[key]
	if !exists {
		return "", "", false, false
	}
	if time.Now().After(entry.expiresAt) {
		c.deleteLocked(key, entry)
		return "", "", false, false
	}
	return entry.content, entry.title, entry.truncated, true
}

// Stores content/title, evicting random entries (not LRU) to fit. Call only after successful fetch.
func (c *referenceURLCache) set(tenantID uint64, url, content, title string, truncated bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := referenceURLCacheKey(tenantID, url)
	if existing, exists := c.entries[key]; exists {
		c.deleteLocked(key, existing)
	}
	entry := cachedReference{content: content, title: title, truncated: truncated, expiresAt: time.Now().Add(referenceURLCacheTTL)}
	for c.totalBytes+entryBytes(entry) > referenceURLCacheMaxBytes && len(c.entries) > 0 {
		for k, v := range c.entries {
			c.deleteLocked(k, v)
			break
		}
	}
	c.entries[key] = entry
	c.totalBytes += entryBytes(entry)
}

// deleteLocked removes key and releases its bytes from totalBytes — c.mu
// must already be held by the caller.
func (c *referenceURLCache) deleteLocked(key string, entry cachedReference) {
	delete(c.entries, key)
	c.totalBytes -= entryBytes(entry)
}

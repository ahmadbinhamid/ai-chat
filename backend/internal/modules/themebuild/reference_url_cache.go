package themebuild

import (
	"fmt"
	"sync"
	"time"

	"ai-chat/internal/urlfetch"
)

// referenceURLCacheTTL bounds how long a fetched page is reused across
// turns. Long enough that a merchant iterating on the same reference
// across several turns in one back-and-forth pays for the fetch once, not
// once per turn; short enough that a page which changes while they're
// working never goes stale for long.
const referenceURLCacheTTL = 5 * time.Minute

// referenceURLCacheMaxEntries bounds referenceURLCache's total size — same
// reasoning and eviction style as historySummaryCache (see its own doc
// comment): one entry per (tenant, URL) pair ever fetched, capped so a
// long-lived process serving many tenants can't grow this map without
// bound (CLAUDE.md rule 8).
const referenceURLCacheMaxEntries = 2048

// referenceURLCacheEntry is one cached fetch result plus when it expires.
type referenceURLCacheEntry struct {
	result    urlfetch.Result
	expiresAt time.Time
}

// referenceURLCache is an in-process, best-effort TTL cache of recently
// fetched reference pages, keyed by tenant ID PLUS the URL (see
// referenceURLCacheKey) — never the bare URL alone (CLAUDE.md rule 9): a
// URL is client-influenced text, so two different tenants referencing the
// same public page must not share a cache entry, or one tenant's cache
// lifetime/eviction becomes entangled with another's unrelated traffic.
//
// Cache SUCCESSES only — see set's own doc comment for why a failure is
// never stored. Correctness never depends on a hit: a cold cache after a
// restart, a miss on a different replica, or an evicted/expired entry just
// costs one extra fetch, exactly like historySummaryCache's own contract.
type referenceURLCache struct {
	mu      sync.Mutex
	entries map[string]referenceURLCacheEntry
}

func newReferenceURLCache() *referenceURLCache {
	return &referenceURLCache{entries: make(map[string]referenceURLCacheEntry)}
}

func referenceURLCacheKey(tenantID uint64, url string) string {
	return fmt.Sprintf("%d:%s", tenantID, url)
}

// get returns url's cached result for tenantID, if any and not yet
// expired. An expired entry is deleted right here on read rather than left
// for a separate sweep — this cache has no background goroutine of its
// own, and self-pruning on access is enough to keep it from accumulating
// stale entries indefinitely between the cap-triggered evictions in set.
func (c *referenceURLCache) get(tenantID uint64, url string) (urlfetch.Result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := referenceURLCacheKey(tenantID, url)
	entry, ok := c.entries[key]
	if !ok {
		return urlfetch.Result{}, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(c.entries, key)
		return urlfetch.Result{}, false
	}
	return entry.result, true
}

// set stores result for (tenantID, url), evicting one arbitrary entry
// first if already at referenceURLCacheMaxEntries (Go map iteration order
// is randomized, so this is effectively random eviction, not LRU — good
// enough for a best-effort cache, same tradeoff historySummaryCache makes).
// Callers must only call this after a SUCCESSFUL fetch: a failed fetch must
// be retried on the merchant's very next turn, not remembered as a dead
// end for the rest of referenceURLCacheTTL.
func (c *referenceURLCache) set(tenantID uint64, url string, result urlfetch.Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := referenceURLCacheKey(tenantID, url)
	if _, exists := c.entries[key]; !exists && len(c.entries) >= referenceURLCacheMaxEntries {
		for k := range c.entries {
			delete(c.entries, k)
			break
		}
	}
	c.entries[key] = referenceURLCacheEntry{result: result, expiresAt: time.Now().Add(referenceURLCacheTTL)}
}

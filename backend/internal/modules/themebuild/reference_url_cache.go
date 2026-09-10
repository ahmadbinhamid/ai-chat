package themebuild

import (
	"fmt"
	"sync"
	"time"
)

// referenceURLCacheTTL bounds how long a fetched page is reused across
// turns. Long enough that a merchant iterating on the same reference
// across several turns in one back-and-forth pays for the fetch once, not
// once per turn; short enough that a page which changes while they're
// working never goes stale for long.
const referenceURLCacheTTL = 5 * time.Minute

// referenceURLCacheMaxBytes bounds referenceURLCache's total size in BYTES
// of cached content, not a count of entries. historySummaryCache (whose
// count-based cap this cache originally borrowed) holds short summary
// strings, where a count cap and a byte cap amount to the same thing in
// practice; an entry here holds a whole fetched-and-sanitized page body,
// which is unbounded-in-practice per entry (up to PostStripMaxBytes,
// ~300KB, each) — a count cap of, say, 2048 entries would still allow a
// ~600MB worst case, satisfying CLAUDE.md rule 8 on paper while violating
// it in practice. 64MB of ≤300KB entries is several hundred pages,
// comfortably more than one merchant's session needs live in cache at
// once.
const referenceURLCacheMaxBytes = 64 * 1024 * 1024

// cachedReference is what referenceURLCache actually stores — the
// SANITIZED, already-truncated content doGenerate hands the model (see
// Service.fetchReferenceURL, the only place that builds one of these), not
// urlfetch.Result's raw pre-sanitize body. Caching the finished content
// means a hit skips SanitizeHTMLAttachment entirely — strictly faster than
// a miss, not just smaller to store — and bounds each entry at
// PostStripMaxBytes instead of the much larger raw-fetch cap.
type cachedReference struct {
	content   string
	truncated bool
	expiresAt time.Time
}

// referenceURLCache is an in-process, best-effort TTL cache of recently
// fetched-and-sanitized reference pages, keyed by tenant ID PLUS the URL
// (see referenceURLCacheKey) — never the bare URL alone (CLAUDE.md rule
// 9): a URL is client-influenced text, so two different tenants
// referencing the same public page must not share a cache entry, or one
// tenant's cache lifetime/eviction becomes entangled with another's
// unrelated traffic.
//
// Cache SUCCESSES only — see set's own doc comment for why a failure is
// never stored. Correctness never depends on a hit: a cold cache after a
// restart, a miss on a different replica, or an evicted/expired entry just
// costs one extra fetch (plus a re-sanitize), exactly like
// historySummaryCache's own contract.
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

// get returns url's cached (sanitized, truncated) content for tenantID, if
// any and not yet expired. An expired entry is deleted — and its bytes
// released from totalBytes — right here on read rather than left for a
// separate sweep; this cache has no background goroutine of its own, and
// self-pruning on access is enough to keep it from accumulating stale
// entries indefinitely between the cap-triggered evictions in set.
func (c *referenceURLCache) get(tenantID uint64, url string) (content string, truncated bool, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := referenceURLCacheKey(tenantID, url)
	entry, exists := c.entries[key]
	if !exists {
		return "", false, false
	}
	if time.Now().After(entry.expiresAt) {
		c.deleteLocked(key, entry)
		return "", false, false
	}
	return entry.content, entry.truncated, true
}

// set stores content for (tenantID, url), evicting entries (Go map
// iteration order is randomized — effectively random eviction, not LRU,
// the same tradeoff historySummaryCache makes) until the new entry fits
// within referenceURLCacheMaxBytes. Callers must only call this after a
// SUCCESSFUL fetch+sanitize: a failure must be retried on the merchant's
// very next turn, not remembered as a dead end for referenceURLCacheTTL.
func (c *referenceURLCache) set(tenantID uint64, url, content string, truncated bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := referenceURLCacheKey(tenantID, url)
	if existing, exists := c.entries[key]; exists {
		c.deleteLocked(key, existing)
	}
	for c.totalBytes+len(content) > referenceURLCacheMaxBytes && len(c.entries) > 0 {
		for k, v := range c.entries {
			c.deleteLocked(k, v)
			break
		}
	}
	c.entries[key] = cachedReference{content: content, truncated: truncated, expiresAt: time.Now().Add(referenceURLCacheTTL)}
	c.totalBytes += len(content)
}

// deleteLocked removes key and releases its bytes from totalBytes — c.mu
// must already be held by the caller.
func (c *referenceURLCache) deleteLocked(key string, entry cachedReference) {
	delete(c.entries, key)
	c.totalBytes -= len(entry.content)
}

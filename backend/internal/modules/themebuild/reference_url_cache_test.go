package themebuild

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// These exercise fetchReferenceURL directly (bare *Service, no database) —
// the cache itself is pure in-process logic, so a real DB/network stack
// adds nothing to what these prove; the end-to-end path (a real doGenerate
// call actually consulting it) is covered separately in
// attachment_doGenerate_test.go.

func TestFetchReferenceURL_CachesWithinTTLForSameTenant(t *testing.T) {
	fl := &fakeLinkFetcher{content: "<h1>cached</h1>"}
	svc := &Service{links: fl, linkCache: newReferenceURLCache()}
	ctx := context.Background()
	const tenantID = uint64(1)

	firstContent, _, err := svc.fetchReferenceURL(ctx, tenantID, "https://example.com", 1024)
	if err != nil {
		t.Fatalf("first fetch failed: %v", err)
	}
	secondContent, _, err := svc.fetchReferenceURL(ctx, tenantID, "https://example.com", 1024)
	if err != nil {
		t.Fatalf("second fetch failed: %v", err)
	}

	if fl.calls != 1 {
		t.Errorf("expected exactly 1 real fetch call (second served from cache), got %d", fl.calls)
	}
	if secondContent != firstContent {
		t.Errorf("expected the cached result to match the original: got %q, want %q", secondContent, firstContent)
	}
}

// TestFetchReferenceURL_CacheHitSkipsSanitize proves a cache hit returns
// the ALREADY-sanitized content straight from the cache rather than
// re-running SanitizeHTMLAttachment on it — the latency point of caching
// post-sanitize content in the first place (see fetchReferenceURL's own
// doc comment). Sanitizing is idempotent, so this can't be proven by
// re-sanitizing the result and comparing; instead it seeds the cache
// directly with content SanitizeHTMLAttachment would never itself produce
// (a live <script> tag, which SanitizeHTMLAttachment strips) and confirms
// a hit returns that content completely unchanged.
func TestFetchReferenceURL_CacheHitSkipsSanitize(t *testing.T) {
	const tenantID = uint64(1)
	const url = "https://example.com"
	const rawWithScript = `<h1>hi</h1><script>alert(1)</script>`

	cache := newReferenceURLCache()
	cache.set(tenantID, url, rawWithScript, false)
	fl := &fakeLinkFetcher{content: "<h1>should not be used</h1>"}
	svc := &Service{links: fl, linkCache: cache}

	got, _, err := svc.fetchReferenceURL(context.Background(), tenantID, url, 1024)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if fl.calls != 0 {
		t.Errorf("expected a cache hit to make 0 real fetch calls, got %d", fl.calls)
	}
	if got != rawWithScript {
		t.Errorf("expected a cache hit to skip sanitizing and return the stored content verbatim: got %q, want %q", got, rawWithScript)
	}
	if !strings.Contains(got, "<script>") {
		t.Error("expected the unsanitized <script> tag to survive a cache hit untouched — proves sanitize did not re-run")
	}
}

func TestFetchReferenceURL_DoesNotShareCacheAcrossTenants(t *testing.T) {
	fl := &fakeLinkFetcher{content: "<h1>content</h1>"}
	svc := &Service{links: fl, linkCache: newReferenceURLCache()}
	ctx := context.Background()

	if _, _, err := svc.fetchReferenceURL(ctx, 1, "https://example.com", 1024); err != nil {
		t.Fatalf("tenant 1 fetch failed: %v", err)
	}
	if _, _, err := svc.fetchReferenceURL(ctx, 2, "https://example.com", 1024); err != nil {
		t.Fatalf("tenant 2 fetch failed: %v", err)
	}

	if fl.calls != 2 {
		t.Errorf("expected 2 real fetch calls (one per tenant — CLAUDE.md rule 9, never a bare-URL cache key), got %d", fl.calls)
	}
}

func TestFetchReferenceURL_DoesNotCacheFailure(t *testing.T) {
	fl := &fakeLinkFetcher{err: errors.New("simulated fetch failure")}
	svc := &Service{links: fl, linkCache: newReferenceURLCache()}
	ctx := context.Background()
	const tenantID = uint64(1)

	if _, _, err := svc.fetchReferenceURL(ctx, tenantID, "https://example.com", 1024); err == nil {
		t.Fatal("expected the first fetch to fail")
	}
	if _, _, err := svc.fetchReferenceURL(ctx, tenantID, "https://example.com", 1024); err == nil {
		t.Fatal("expected the second fetch to also fail — a failure must never be served from cache")
	}

	if fl.calls != 2 {
		t.Errorf("expected 2 real fetch calls (a failure is never cached — the merchant's next turn must retry), got %d", fl.calls)
	}
}

func TestFetchReferenceURL_NilCacheFallsBackToUncached(t *testing.T) {
	fl := &fakeLinkFetcher{content: "<h1>ok</h1>"}
	svc := &Service{links: fl} // linkCache left nil, same nil-guard convention as links itself
	ctx := context.Background()

	if _, _, err := svc.fetchReferenceURL(ctx, 1, "https://example.com", 1024); err != nil {
		t.Fatalf("expected a nil linkCache to fall back to an uncached call, got error: %v", err)
	}
	if _, _, err := svc.fetchReferenceURL(ctx, 1, "https://example.com", 1024); err != nil {
		t.Fatalf("expected a nil linkCache to fall back to an uncached call, got error: %v", err)
	}
	if fl.calls != 2 {
		t.Errorf("expected 2 real fetch calls with no cache in place, got %d", fl.calls)
	}
}

// TestReferenceURLCache_ExpiresAfterTTL exercises the cache type directly
// (not through fetchReferenceURL) to prove an entry past its TTL is a miss —
// see set's own doc comment for why the entry is stamped with
// referenceURLCacheTTL, not tested by waiting referenceURLCacheTTL for real.
func TestReferenceURLCache_ExpiresAfterTTL(t *testing.T) {
	c := newReferenceURLCache()
	c.entries[referenceURLCacheKey(1, "https://example.com")] = cachedReference{
		content:   "<h1>stale</h1>",
		expiresAt: time.Now().Add(-time.Minute),
	}
	if _, _, ok := c.get(1, "https://example.com"); ok {
		t.Error("expected an expired entry to be a cache miss")
	}
	if _, ok := c.entries[referenceURLCacheKey(1, "https://example.com")]; ok {
		t.Error("expected the expired entry to be pruned from the map on read, not just ignored")
	}
}

// TestReferenceURLCache_EvictsByTotalBytesNotEntryCount proves the cache
// bounds its memory by the total BYTES of cached content, not a count of
// entries — the whole point of moving off the old entry-count cap (2048
// entries of up to 5MB raw body each was a ~10GB worst case; see
// referenceURLCacheMaxBytes' own doc comment). Inserts enough entries to
// exceed referenceURLCacheMaxBytes several times over and asserts the
// running total never exceeds the cap and the earliest entries are gone.
func TestReferenceURLCache_EvictsByTotalBytesNotEntryCount(t *testing.T) {
	c := newReferenceURLCache()
	const entrySize = 1024 * 1024 // 1MB
	const entryCount = referenceURLCacheMaxBytes/entrySize*2 + 4

	content := strings.Repeat("a", entrySize)
	for i := 0; i < entryCount; i++ {
		url := "https://example.com/" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		c.set(1, url, content, false)
		if c.totalBytes > referenceURLCacheMaxBytes {
			t.Fatalf("after inserting entry %d, totalBytes = %d exceeds cap %d", i, c.totalBytes, referenceURLCacheMaxBytes)
		}
	}

	if c.totalBytes > referenceURLCacheMaxBytes {
		t.Errorf("totalBytes = %d exceeds referenceURLCacheMaxBytes = %d", c.totalBytes, referenceURLCacheMaxBytes)
	}
	if len(c.entries) >= entryCount {
		t.Errorf("expected older entries to have been evicted, but all %d entries are still present", len(c.entries))
	}
	// Eviction is arbitrary (Go map iteration order), not LRU — see
	// referenceURLCache.set's own doc comment — so which specific entries
	// survive isn't asserted, only that the cap held and something gave way.
}

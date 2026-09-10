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

// fetchReferenceURLTestHTML is real enough markup (a title, a heading, and
// a paragraph) to build a NON-empty digest — the cache only ever stores a
// non-empty digest (see fetchReferenceURL's own doc comment), so any test
// below that needs an entry to actually get cached uses this rather than a
// bare fragment that might digest to nothing. The <title> lets
// TestFetchReferenceURL_CachesWithinTTLForSameTenant also confirm the
// title round-trips through a cache hit end to end (not just via a
// manually seeded entry — see TestFetchReferenceURL_CacheHitSkipsRebuildingDigest
// for that).
const fetchReferenceURLTestHTML = `<html><head><title>Cached Reference Page</title></head>` +
	`<body><h1>cached</h1><p>Some real body copy so the digest isn't empty.</p></body></html>`

func TestFetchReferenceURL_CachesWithinTTLForSameTenant(t *testing.T) {
	fl := &fakeLinkFetcher{content: fetchReferenceURLTestHTML}
	svc := &Service{links: fl, linkCache: newReferenceURLCache()}
	ctx := context.Background()
	const tenantID = uint64(1)

	firstContent, _, _, firstTitle, _, err := svc.fetchReferenceURL(ctx, tenantID, "https://example.com", 1024)
	if err != nil {
		t.Fatalf("first fetch failed: %v", err)
	}
	secondContent, _, _, secondTitle, _, err := svc.fetchReferenceURL(ctx, tenantID, "https://example.com", 1024)
	if err != nil {
		t.Fatalf("second fetch failed: %v", err)
	}

	if fl.calls != 1 {
		t.Errorf("expected exactly 1 real fetch call (second served from cache), got %d", fl.calls)
	}
	if secondContent != firstContent {
		t.Errorf("expected the cached result to match the original: got %q, want %q", secondContent, firstContent)
	}
	if firstTitle != "Cached Reference Page" {
		t.Errorf("expected the first (uncached) fetch to report the real page title, got %q", firstTitle)
	}
	if secondTitle != firstTitle {
		t.Errorf("expected the cache hit to report the same title as the original fetch: got %q, want %q", secondTitle, firstTitle)
	}
}

// TestFetchReferenceURL_CacheHitSkipsRebuildingDigest proves a cache hit
// returns the ALREADY-built digest (and title) straight from the cache
// rather than re-fetching stylesheets and re-running BuildDigest on it —
// the latency point of caching the finished digest in the first place (see
// fetchReferenceURL's own doc comment). Seeds the cache directly with text
// BuildDigest would never itself produce from fl's configured HTML, and
// confirms a hit returns that text (and title) completely unchanged while
// making zero calls to Fetch (and, transitively, FetchStylesheets/
// BuildDigest).
func TestFetchReferenceURL_CacheHitSkipsRebuildingDigest(t *testing.T) {
	const tenantID = uint64(1)
	const url = "https://example.com"
	const cachedDigest = "PAGE\nurl: https://example.com/\ntitle: A Manually Seeded Cache Entry\n"
	const cachedTitle = "A Manually Seeded Cache Entry"

	cache := newReferenceURLCache()
	cache.set(tenantID, url, cachedDigest, cachedTitle, false)
	fl := &fakeLinkFetcher{content: "<h1>should never be fetched or digested</h1>"}
	svc := &Service{links: fl, linkCache: cache}

	content, _, empty, title, styleCount, err := svc.fetchReferenceURL(context.Background(), tenantID, url, 1024)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if fl.calls != 0 {
		t.Errorf("expected a cache hit to make 0 real fetch calls, got %d", fl.calls)
	}
	if content != cachedDigest {
		t.Errorf("expected a cache hit to return the stored digest verbatim: got %q, want %q", content, cachedDigest)
	}
	if empty {
		t.Error("expected a cache hit to never report empty — only a non-empty digest is ever cached")
	}
	// title DOES ride along on a cache hit (Phase 3.1) — see
	// cachedReference's own doc comment for why. styleCount does not: a
	// hit fetches nothing new this turn, so 0 is the accurate count.
	if title != cachedTitle {
		t.Errorf("expected a cache hit to return the cached title, got %q, want %q", title, cachedTitle)
	}
	if styleCount != 0 {
		t.Errorf("expected a cache hit to report styleCount = 0, got %d", styleCount)
	}
}

func TestFetchReferenceURL_DoesNotShareCacheAcrossTenants(t *testing.T) {
	fl := &fakeLinkFetcher{content: fetchReferenceURLTestHTML}
	svc := &Service{links: fl, linkCache: newReferenceURLCache()}
	ctx := context.Background()

	if _, _, _, _, _, err := svc.fetchReferenceURL(ctx, 1, "https://example.com", 1024); err != nil {
		t.Fatalf("tenant 1 fetch failed: %v", err)
	}
	if _, _, _, _, _, err := svc.fetchReferenceURL(ctx, 2, "https://example.com", 1024); err != nil {
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

	if _, _, _, _, _, err := svc.fetchReferenceURL(ctx, tenantID, "https://example.com", 1024); err == nil {
		t.Fatal("expected the first fetch to fail")
	}
	if _, _, _, _, _, err := svc.fetchReferenceURL(ctx, tenantID, "https://example.com", 1024); err == nil {
		t.Fatal("expected the second fetch to also fail — a failure must never be served from cache")
	}

	if fl.calls != 2 {
		t.Errorf("expected 2 real fetch calls (a failure is never cached — the merchant's next turn must retry), got %d", fl.calls)
	}
}

func TestFetchReferenceURL_NilCacheFallsBackToUncached(t *testing.T) {
	fl := &fakeLinkFetcher{content: fetchReferenceURLTestHTML}
	svc := &Service{links: fl} // linkCache left nil, same nil-guard convention as links itself
	ctx := context.Background()

	if _, _, _, _, _, err := svc.fetchReferenceURL(ctx, 1, "https://example.com", 1024); err != nil {
		t.Fatalf("expected a nil linkCache to fall back to an uncached call, got error: %v", err)
	}
	if _, _, _, _, _, err := svc.fetchReferenceURL(ctx, 1, "https://example.com", 1024); err != nil {
		t.Fatalf("expected a nil linkCache to fall back to an uncached call, got error: %v", err)
	}
	if fl.calls != 2 {
		t.Errorf("expected 2 real fetch calls with no cache in place, got %d", fl.calls)
	}
}

// TestReferenceURLCache_TotalBytesCountsTitle confirms title bytes are
// included in totalBytes (both on insert and on release), not just
// content's — an accounting gap here wouldn't cause a real memory problem
// on its own (a title is a handful of bytes next to a ~16KB digest), but
// it would make totalBytes silently drift from what the cache actually
// holds, which is worth catching directly rather than only as a side
// effect of some other test.
func TestReferenceURLCache_TotalBytesCountsTitle(t *testing.T) {
	c := newReferenceURLCache()
	const content = "PAGE\n"
	const title = "A Page With A Title"

	c.set(1, "https://example.com", content, title, false)
	if want := len(content) + len(title); c.totalBytes != want {
		t.Errorf("expected totalBytes = %d (content + title), got %d", want, c.totalBytes)
	}

	c.set(1, "https://example.com", content, "", false)
	if c.totalBytes != len(content) {
		t.Errorf("expected totalBytes to drop back to just content's length after overwriting with an empty title, got %d", c.totalBytes)
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
		title:     "Stale Page",
		expiresAt: time.Now().Add(-time.Minute),
	}
	if _, _, _, ok := c.get(1, "https://example.com"); ok {
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
		c.set(1, url, content, "", false)
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

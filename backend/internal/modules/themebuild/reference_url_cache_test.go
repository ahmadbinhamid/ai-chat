package themebuild

import (
	"context"
	"errors"
	"testing"
	"time"

	"ai-chat/internal/urlfetch"
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

	first, err := svc.fetchReferenceURL(ctx, tenantID, "https://example.com", 1024)
	if err != nil {
		t.Fatalf("first fetch failed: %v", err)
	}
	second, err := svc.fetchReferenceURL(ctx, tenantID, "https://example.com", 1024)
	if err != nil {
		t.Fatalf("second fetch failed: %v", err)
	}

	if fl.calls != 1 {
		t.Errorf("expected exactly 1 real fetch call (second served from cache), got %d", fl.calls)
	}
	if second.HTML != first.HTML {
		t.Errorf("expected the cached result to match the original: got %q, want %q", second.HTML, first.HTML)
	}
}

func TestFetchReferenceURL_DoesNotShareCacheAcrossTenants(t *testing.T) {
	fl := &fakeLinkFetcher{content: "<h1>content</h1>"}
	svc := &Service{links: fl, linkCache: newReferenceURLCache()}
	ctx := context.Background()

	if _, err := svc.fetchReferenceURL(ctx, 1, "https://example.com", 1024); err != nil {
		t.Fatalf("tenant 1 fetch failed: %v", err)
	}
	if _, err := svc.fetchReferenceURL(ctx, 2, "https://example.com", 1024); err != nil {
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

	if _, err := svc.fetchReferenceURL(ctx, tenantID, "https://example.com", 1024); err == nil {
		t.Fatal("expected the first fetch to fail")
	}
	if _, err := svc.fetchReferenceURL(ctx, tenantID, "https://example.com", 1024); err == nil {
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

	if _, err := svc.fetchReferenceURL(ctx, 1, "https://example.com", 1024); err != nil {
		t.Fatalf("expected a nil linkCache to fall back to an uncached call, got error: %v", err)
	}
	if _, err := svc.fetchReferenceURL(ctx, 1, "https://example.com", 1024); err != nil {
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
	c.entries[referenceURLCacheKey(1, "https://example.com")] = referenceURLCacheEntry{
		result:    urlfetch.Result{HTML: "<h1>stale</h1>"},
		expiresAt: time.Now().Add(-time.Minute),
	}
	if _, ok := c.get(1, "https://example.com"); ok {
		t.Error("expected an expired entry to be a cache miss")
	}
	if _, ok := c.entries[referenceURLCacheKey(1, "https://example.com")]; ok {
		t.Error("expected the expired entry to be pruned from the map on read, not just ignored")
	}
}

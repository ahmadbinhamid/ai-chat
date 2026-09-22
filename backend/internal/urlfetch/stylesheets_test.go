package urlfetch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestExtractStylesheetSources(t *testing.T) {
	finalURL, err := url.Parse("https://example.com/shop/")
	if err != nil {
		t.Fatalf("test setup: %v", err)
	}

	tests := []struct {
		name          string
		html          string
		wantHrefs     []string
		wantInlineCSS string
	}{
		{
			name:      "relative href resolves against finalURL",
			html:      `<html><head><link rel="stylesheet" href="style.css"></head></html>`,
			wantHrefs: []string{"https://example.com/shop/style.css"},
		},
		{
			name:      "absolute href kept as-is",
			html:      `<link rel="stylesheet" href="https://cdn.example.com/a.css">`,
			wantHrefs: []string{"https://cdn.example.com/a.css"},
		},
		{
			name:      "protocol-relative href inherits scheme from finalURL",
			html:      `<link rel="stylesheet" href="//cdn.example.com/b.css">`,
			wantHrefs: []string{"https://cdn.example.com/b.css"},
		},
		{
			name:      "base href changes relative resolution",
			html:      `<base href="https://other.example.com/assets/"><link rel="stylesheet" href="x.css">`,
			wantHrefs: []string{"https://other.example.com/assets/x.css"},
		},
		{
			name:      "multi-token rel matches the stylesheet token",
			html:      `<link rel="preload stylesheet" href="pre.css">`,
			wantHrefs: []string{"https://example.com/shop/pre.css"},
		},
		{
			name:      "rel without the stylesheet token is ignored",
			html:      `<link rel="preload" href="icon.png">`,
			wantHrefs: nil,
		},
		{
			name:          "inline style content is collected",
			html:          `<style>.a{color:red}</style>`,
			wantInlineCSS: ".a{color:red}",
		},
		{
			name: "more than maxStylesheets hrefs is capped, in document order",
			html: `<link rel="stylesheet" href="a.css"><link rel="stylesheet" href="b.css">` +
				`<link rel="stylesheet" href="c.css"><link rel="stylesheet" href="d.css">`,
			wantHrefs: []string{
				"https://example.com/shop/a.css",
				"https://example.com/shop/b.css",
				"https://example.com/shop/c.css",
			},
		},
		{
			name:      "duplicate resolved href is deduped",
			html:      `<link rel="stylesheet" href="a.css"><link rel="stylesheet" href="a.css">`,
			wantHrefs: []string{"https://example.com/shop/a.css"},
		},
		{
			name:      "no link tags at all",
			html:      `<html><body><h1>hi</h1></body></html>`,
			wantHrefs: nil,
		},
		{
			name:      "link with media=print is skipped",
			html:      `<link rel="stylesheet" href="print.css" media="print">`,
			wantHrefs: nil,
		},
		{
			name:      "link with a screen media query is kept",
			html:      `<link rel="stylesheet" href="a.css" media="screen and (min-width: 800px)">`,
			wantHrefs: []string{"https://example.com/shop/a.css"},
		},
		{
			name:      "link with no media attribute is kept",
			html:      `<link rel="stylesheet" href="a.css">`,
			wantHrefs: []string{"https://example.com/shop/a.css"},
		},
		{
			name:          "style with media=print is skipped",
			html:          `<style media="print">.a{color:red}</style>`,
			wantInlineCSS: "",
		},
		{
			name:          "style with no media attribute is kept",
			html:          `<style>.a{color:red}</style>`,
			wantInlineCSS: ".a{color:red}",
		},
		{
			name:          "style nested inside an inline svg is skipped",
			html:          `<svg><style>.icon{fill:red}</style></svg><style>.page{color:blue}</style>`,
			wantInlineCSS: ".page{color:blue}",
		},
		{
			name:          "style after a self-closing svg is kept, not treated as nested",
			html:          `<svg/><style>.page{color:blue}</style>`,
			wantInlineCSS: ".page{color:blue}",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hrefs, inlineCSS := extractStylesheetSources(tt.html, finalURL)
			if !slices.Equal(hrefs, tt.wantHrefs) {
				t.Errorf("hrefs = %v, want %v", hrefs, tt.wantHrefs)
			}
			if inlineCSS != tt.wantInlineCSS {
				t.Errorf("inlineCSS = %q, want %q", inlineCSS, tt.wantInlineCSS)
			}
		})
	}
}

func TestCombineCSS_StopsAccumulatingAtBudget(t *testing.T) {
	got, contributed := combineCSS(10, []string{"12345", "67890ABCDE"})
	if got != "1234567890" {
		t.Errorf("got %q, want %q", got, "1234567890")
	}
	if contributed != 2 {
		t.Errorf("expected both chunks to have contributed bytes, got contributed = %d", contributed)
	}
}

func TestCombineCSS_SkipsChunksAfterBudgetExactlySpent(t *testing.T) {
	got, contributed := combineCSS(5, []string{"12345", "should not appear"})
	if got != "12345" {
		t.Errorf("got %q, want %q", got, "12345")
	}
	// The second chunk got no bytes in once the first exactly spent the
	// budget, so it must not count as contributed.
	if contributed != 1 {
		t.Errorf("expected only the first chunk to have contributed bytes, got contributed = %d", contributed)
	}
}

// TestCombineCSS_EnforcesTheRealByteCap exercises combineCSS against the
// actual maxStylesheetBytes const, not an arbitrary small number.
func TestCombineCSS_EnforcesTheRealByteCap(t *testing.T) {
	big := strings.Repeat("a", maxStylesheetBytes+1000)
	got, contributed := combineCSS(maxStylesheetBytes, []string{big})
	if len(got) > maxStylesheetBytes {
		t.Errorf("expected at most %d bytes, got %d", maxStylesheetBytes, len(got))
	}
	if len(got) != maxStylesheetBytes {
		t.Errorf("expected exactly %d bytes for an all-ASCII chunk over budget, got %d", maxStylesheetBytes, len(got))
	}
	if contributed != 1 {
		t.Errorf("expected the one (truncated) chunk to still count as contributed, got %d", contributed)
	}
}

// TestFetchStylesheets_CapsEachFileAtItsOwnShare confirms a SINGLE
// stylesheet larger than maxStylesheetBytesPerFile is cut to its own share,
// not read in full — needed so maxStylesheets racing concurrently can't
// together exceed maxStylesheetBytes off the wire.
func TestFetchStylesheets_CapsEachFileAtItsOwnShare(t *testing.T) {
	over := strings.Repeat("a", maxStylesheetBytesPerFile+1000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		_, _ = w.Write([]byte(over))
	}))
	defer srv.Close()

	htmlSrc := fmt.Sprintf(`<link rel="stylesheet" href="%s/big.css">`, srv.URL)
	finalURL, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatalf("test setup: %v", err)
	}

	f := newFetcherWithDialContext(unguardedDialContext)
	css, count := f.FetchStylesheets(context.Background(), htmlSrc, finalURL)

	if len(css) > maxStylesheetBytesPerFile {
		t.Errorf("expected at most %d bytes from one oversized stylesheet, got %d", maxStylesheetBytesPerFile, len(css))
	}
	if count != 1 {
		t.Errorf("expected count = 1 (the fetch still succeeded, just truncated), got %d", count)
	}
}

// TestFetchStylesheets_RunsConcurrently proves the fetches happen at once,
// not serially, via a channel-based rendezvous rather than a wall-clock
// threshold: every request blocks until all n requests have arrived, which
// a serial fetcher could never satisfy — it would stall until
// stylesheetPhaseTimeout, failing this test's count assertion structurally.
func TestFetchStylesheets_RunsConcurrently(t *testing.T) {
	const n = 3
	var mu sync.Mutex
	arrived := 0
	allArrived := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		arrived++
		reachedN := arrived == n
		mu.Unlock()
		if reachedN {
			close(allArrived)
		}
		select {
		case <-allArrived:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/css")
		_, _ = w.Write([]byte(".x{color:red}"))
	}))
	defer srv.Close()

	htmlSrc := fmt.Sprintf(
		`<link rel="stylesheet" href="%s/a.css"><link rel="stylesheet" href="%s/b.css"><link rel="stylesheet" href="%s/c.css">`,
		srv.URL, srv.URL, srv.URL,
	)
	finalURL, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatalf("test setup: %v", err)
	}

	f := newFetcherWithDialContext(unguardedDialContext)
	css, count := f.FetchStylesheets(context.Background(), htmlSrc, finalURL)

	if got := strings.Count(css, "color:red"); got != n {
		t.Errorf("expected all %d concurrent stylesheets to be collected, got %d occurrences in: %q", n, got, css)
	}
	if count != n {
		t.Errorf("expected count = %d, got %d", n, count)
	}
}

// TestFetchStylesheets_DoesNotCountAStylesheetThatDidNotSurviveTheBudget: a
// stylesheet whose fetch succeeds can still contribute zero bytes if the
// budget was already spent by earlier chunks (inline CSS goes first) —
// count must reflect that, not just that the HTTP request succeeded.
func TestFetchStylesheets_DoesNotCountAStylesheetThatDidNotSurviveTheBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		_, _ = w.Write([]byte(".good{color:blue}"))
	}))
	defer srv.Close()

	// Inline CSS alone spends the ENTIRE budget, so the external stylesheet
	// below gets nothing even though its own fetch succeeds.
	inline := strings.Repeat("a", maxStylesheetBytes)
	htmlSrc := fmt.Sprintf(`<style>%s</style><link rel="stylesheet" href="%s/small.css">`, inline, srv.URL)
	finalURL, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatalf("test setup: %v", err)
	}

	f := newFetcherWithDialContext(unguardedDialContext)
	css, count := f.FetchStylesheets(context.Background(), htmlSrc, finalURL)

	if strings.Contains(css, "color:blue") {
		t.Errorf("expected the external stylesheet's content to be fully crowded out by inline CSS, but found it in: (css omitted, %d bytes)", len(css))
	}
	if count != 0 {
		t.Errorf("expected count = 0 — the stylesheet fetched fine but contributed no bytes, got %d", count)
	}
}

// TestFetchStylesheets_InlineCSSDoesNotCountTowardStylesheetCount confirms
// inline <style> content is collected but never counted as a "stylesheet
// fetched" — it wasn't fetched, it was already in the document.
func TestFetchStylesheets_InlineCSSDoesNotCountTowardStylesheetCount(t *testing.T) {
	htmlSrc := `<style>.a{color:red}</style>`
	finalURL, err := url.Parse("https://example.com/")
	if err != nil {
		t.Fatalf("test setup: %v", err)
	}

	f := newFetcherWithDialContext(unguardedDialContext)
	css, count := f.FetchStylesheets(context.Background(), htmlSrc, finalURL)

	if !strings.Contains(css, "color:red") {
		t.Errorf("expected inline CSS to still be collected, got: %q", css)
	}
	if count != 0 {
		t.Errorf("expected count = 0 for inline-only CSS (nothing was fetched), got %d", count)
	}
}

// TestFetchStylesheets_SkipsFailingStylesheetWithoutFailingOthers confirms
// a 500 from one stylesheet host costs nothing but that stylesheet's own
// content — CSS is an enhancement, never a reason to lose the others.
func TestFetchStylesheets_SkipsFailingStylesheetWithoutFailingOthers(t *testing.T) {
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer badSrv.Close()
	goodSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		_, _ = w.Write([]byte(".good{color:blue}"))
	}))
	defer goodSrv.Close()

	htmlSrc := fmt.Sprintf(
		`<link rel="stylesheet" href="%s/bad.css"><link rel="stylesheet" href="%s/good.css">`,
		badSrv.URL, goodSrv.URL,
	)
	finalURL, err := url.Parse(goodSrv.URL + "/")
	if err != nil {
		t.Fatalf("test setup: %v", err)
	}

	f := newFetcherWithDialContext(unguardedDialContext)
	css, count := f.FetchStylesheets(context.Background(), htmlSrc, finalURL)

	if !strings.Contains(css, "color:blue") {
		t.Errorf("expected the good stylesheet's content in the result, got: %q", css)
	}
	if count != 1 {
		t.Errorf("expected count = 1 (only the good stylesheet succeeded), got %d", count)
	}
}

// TestFetchStylesheets_SkipsBlockedHost confirms the SSRF guard applies to
// stylesheet hrefs too — uses the REAL NewFetcher(), since httptest.Server
// addresses are always loopback and so always rejected by the real guard.
func TestFetchStylesheets_SkipsBlockedHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		_, _ = w.Write([]byte(".blocked{color:red}"))
	}))
	defer srv.Close()

	htmlSrc := fmt.Sprintf(`<link rel="stylesheet" href="%s/style.css">`, srv.URL)
	finalURL, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatalf("test setup: %v", err)
	}

	f := NewFetcher()
	css, count := f.FetchStylesheets(context.Background(), htmlSrc, finalURL)
	if css != "" {
		t.Errorf("expected a blocked (loopback) stylesheet host to be silently skipped, got: %q", css)
	}
	if count != 0 {
		t.Errorf("expected count = 0 for a blocked host, got %d", count)
	}
}

// TestFetchStylesheets_BoundedByPhaseTimeout confirms the phase gives up at
// stylesheetPhaseTimeout. Unlike the concurrency test, proving a TIMEOUT
// value is honored genuinely requires measuring elapsed time, with a
// generous margin to avoid flaking on a loaded CI box.
func TestFetchStylesheets_BoundedByPhaseTimeout(t *testing.T) {
	block := make(chan struct{}) // never closed
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	htmlSrc := fmt.Sprintf(`<link rel="stylesheet" href="%s/slow.css">`, srv.URL)
	finalURL, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatalf("test setup: %v", err)
	}

	f := newFetcherWithDialContext(unguardedDialContext)
	start := time.Now()
	css, count := f.FetchStylesheets(context.Background(), htmlSrc, finalURL)
	elapsed := time.Since(start)

	if css != "" {
		t.Errorf("expected no CSS collected from a stylesheet that never responds, got: %q", css)
	}
	if count != 0 {
		t.Errorf("expected count = 0 for a stylesheet that never responds, got %d", count)
	}
	if elapsed > stylesheetPhaseTimeout+2*time.Second {
		t.Errorf("expected FetchStylesheets to return at or shortly after stylesheetPhaseTimeout (%v), took %v", stylesheetPhaseTimeout, elapsed)
	}
}

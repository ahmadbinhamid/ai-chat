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
	got := combineCSS(10, []string{"12345", "67890ABCDE"})
	if got != "1234567890" {
		t.Errorf("got %q, want %q", got, "1234567890")
	}
}

func TestCombineCSS_SkipsChunksAfterBudgetExactlySpent(t *testing.T) {
	got := combineCSS(5, []string{"12345", "should not appear"})
	if got != "12345" {
		t.Errorf("got %q, want %q", got, "12345")
	}
}

// TestCombineCSS_EnforcesTheRealByteCap exercises combineCSS against the
// actual maxStylesheetBytes package const (not an arbitrary small number,
// like the two tests above) — the 150KB cap FetchStylesheets' own doc
// comment promises.
func TestCombineCSS_EnforcesTheRealByteCap(t *testing.T) {
	big := strings.Repeat("a", maxStylesheetBytes+1000)
	got := combineCSS(maxStylesheetBytes, []string{big})
	if len(got) > maxStylesheetBytes {
		t.Errorf("expected at most %d bytes, got %d", maxStylesheetBytes, len(got))
	}
	if len(got) != maxStylesheetBytes {
		t.Errorf("expected exactly %d bytes for an all-ASCII chunk over budget, got %d", maxStylesheetBytes, len(got))
	}
}

// TestFetchStylesheets_RunsConcurrently proves the fetches happen at once,
// not serially — with a channel-based rendezvous rather than a wall-clock
// threshold (same pattern as Phase 2.2's rewritten
// TestFetcher_Fetch_RejectsNonHTMLWithoutReadingWholeBody): every request
// blocks until all n=maxStylesheets requests have arrived. A genuinely
// serial fetcher could never satisfy that — the second request can't even
// start until the first one's handler returns, which requires the barrier
// to already be released — so it would stall until FetchStylesheets' own
// stylesheetPhaseTimeout cancels everything, and this test's own count-
// based assertion below (not a duration check) would then fail structurally
// rather than by a close timing margin.
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
// stylesheet hrefs exactly as it does to the document fetch itself — using
// the REAL NewFetcher() (not the test-only unguarded dial), since
// httptest.Server addresses are always loopback and so are always rejected
// by the real guard, the same proof TestFetcher_BlocksLoopback uses for
// Fetch.
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

// TestFetchStylesheets_BoundedByPhaseTimeout confirms the whole phase gives
// up at stylesheetPhaseTimeout rather than hanging on an unresponsive
// stylesheet host. Unlike the concurrency test above, there is no
// channel-based way to prove a TIMEOUT VALUE is honored without measuring
// elapsed time — the thing under test genuinely is a duration — so this
// does assert on wall-clock time, with a generous margin above the 3s
// const to avoid flaking on a loaded CI box.
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

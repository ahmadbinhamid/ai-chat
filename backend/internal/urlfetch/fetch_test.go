package urlfetch

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// unguardedDialContext is the standard library's own dial behavior, with no
// IsBlockedIP check — used only to point a Fetcher at an httptest.Server
// (always loopback) so tests below can exercise Fetch's own logic
// (headers, status/content-type/size handling, redirects) independently of
// the SSRF guard, which is already covered on its own in guard_test.go and
// end-to-end in TestFetcher_BlocksLoopback below.
func unguardedDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

func TestFetcher_BlocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>should never be reached</html>"))
	}))
	defer srv.Close()

	f := NewFetcher() // the real, unmodified constructor — guard active
	_, err := f.Fetch(context.Background(), srv.URL, 1024)
	if !errors.Is(err, ErrBlockedHost) {
		t.Fatalf("expected ErrBlockedHost fetching an httptest (loopback) server through the real guard, got: %v", err)
	}
}

// TestFetcher_BlocksResolvedHostname is TestFetcher_BlocksLoopback's
// counterpart for a HOSTNAME rather than an IP-literal URL — httptest.Server
// URLs are always the literal "127.0.0.1", so that test alone never
// exercises guardedDialer's Control hook against an address the standard
// dialer had to actually resolve first. "localhost" almost universally
// resolves to loopback without needing real network access, so rewriting
// the same server's URL to use it proves the guard still catches a blocked
// address reached via resolution, not just one already spelled out as an IP.
func TestFetcher_BlocksResolvedHostname(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>should never be reached</html>"))
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("failed to parse httptest server URL: %v", err)
	}
	hostnameURL := "http://localhost:" + u.Port() + "/"

	f := NewFetcher() // the real, unmodified constructor — guard active
	_, err = f.Fetch(context.Background(), hostnameURL, 1024)
	if !errors.Is(err, ErrBlockedHost) {
		t.Fatalf("expected ErrBlockedHost fetching a hostname that resolves to loopback, got: %v", err)
	}
}

// TestNewFetcher_SetsTransportSubTimeouts confirms the dial/TLS/header
// sub-budgets are actually wired into the Transport NewFetcher builds — see
// dialTimeout's own doc comment for why a single flat fetchTimeout isn't
// enough on its own (one slow step could consume the whole budget).
func TestNewFetcher_SetsTransportSubTimeouts(t *testing.T) {
	f := NewFetcher()
	tr, ok := f.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", f.client.Transport)
	}
	if tr.TLSHandshakeTimeout != 4*time.Second {
		t.Errorf("expected TLSHandshakeTimeout of 4s, got %v", tr.TLSHandshakeTimeout)
	}
	if tr.ResponseHeaderTimeout != 6*time.Second {
		t.Errorf("expected ResponseHeaderTimeout of 6s, got %v", tr.ResponseHeaderTimeout)
	}
}

func TestFetcher_Fetch_Success(t *testing.T) {
	const body = "<html><body><h1>Reference Site</h1></body></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(body))
	}))
	defer srv.Close()

	f := newFetcherWithDialContext(unguardedDialContext)
	got, err := f.Fetch(context.Background(), srv.URL, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.HTML != body {
		t.Errorf("got %q, want %q", got.HTML, body)
	}
	if got.Truncated {
		t.Error("expected Truncated false for a body under the cap")
	}
}

func TestFetcher_Fetch_SetsUserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Write([]byte("<html></html>"))
	}))
	defer srv.Close()

	f := newFetcherWithDialContext(unguardedDialContext)
	if _, err := f.Fetch(context.Background(), srv.URL, 1024); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotUA != userAgent {
		t.Errorf("got User-Agent %q, want %q", gotUA, userAgent)
	}
}

func TestFetcher_Fetch_RejectsNonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	f := newFetcherWithDialContext(unguardedDialContext)
	_, err := f.Fetch(context.Background(), srv.URL, 1024)
	if !errors.Is(err, ErrFetchFailed) {
		t.Fatalf("expected ErrFetchFailed for a 404 response, got: %v", err)
	}
}

func TestFetcher_Fetch_RejectsNonHTMLContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Write([]byte("%PDF-1.4 not actually a real pdf"))
	}))
	defer srv.Close()

	f := newFetcherWithDialContext(unguardedDialContext)
	_, err := f.Fetch(context.Background(), srv.URL, 1024)
	if !errors.Is(err, ErrNotHTML) {
		t.Fatalf("expected ErrNotHTML for a PDF content-type, got: %v", err)
	}
}

func TestFetcher_Fetch_AllowsMissingContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set(..., "") rather than Del: net/http's own auto-sniffing only
		// triggers when the Content-Type key is ABSENT from the header map
		// (net/http/server.go checks `_, haveType := header["Content-Type"]`)
		// — Del achieves that, but then Go itself sniffs this body (which
		// opens with "<html") and sends a real "text/html" header anyway, so
		// the client never actually sees a missing Content-Type and this
		// test would silently exercise the header branch of looksLikeHTML,
		// not the no-header/sniff branch it's named for. Set(..., "") keeps
		// the key present with an empty value, which suppresses net/http's
		// sniffing and lets an actually-empty header reach the client.
		w.Header().Set("Content-Type", "")
		w.Write([]byte("<html></html>"))
	}))
	defer srv.Close()

	f := newFetcherWithDialContext(unguardedDialContext)
	if _, err := f.Fetch(context.Background(), srv.URL, 1024); err != nil {
		t.Fatalf("unexpected error for a response with no content-type header: %v", err)
	}
}

// TestFetcher_Fetch_TruncatesOverMaxBytes: a body over maxBytes truncates
// instead of failing — see Result.Truncated's own doc comment for why a
// link's size isn't something the merchant controls the way an upload's is.
func TestFetcher_Fetch_TruncatesOverMaxBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>" + strings.Repeat("x", 2000) + "</html>"))
	}))
	defer srv.Close()

	f := newFetcherWithDialContext(unguardedDialContext)
	got, err := f.Fetch(context.Background(), srv.URL, 1000)
	if err != nil {
		t.Fatalf("expected truncation, not an error, for a 2000-byte body against a 1000-byte cap: %v", err)
	}
	if !got.Truncated {
		t.Error("expected Truncated true")
	}
	if len(got.HTML) > 1000 {
		t.Errorf("expected at most 1000 bytes, got %d", len(got.HTML))
	}
}

// TestFetcher_Fetch_TruncatesAtTagBoundary confirms the truncated HTML
// never ends mid-tag — the cut point (position 1000, right in the middle
// of a long attribute value) has no earlier tag close, so it must back up
// to the last unfinished tag's own opening '<' rather than keep a
// half-written one.
func TestFetcher_Fetch_TruncatesAtTagBoundary(t *testing.T) {
	prefix := "<html><body><p>hello</p><div data-x=\""
	// Pad well past 1000 bytes so the cut point genuinely lands inside the
	// long attribute value, not by coincidence right at a tag boundary.
	body := prefix + strings.Repeat("a", 2000) + "\"></div></body></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	f := newFetcherWithDialContext(unguardedDialContext)
	got, err := f.Fetch(context.Background(), srv.URL, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Truncated {
		t.Fatal("expected Truncated true")
	}
	if strings.HasSuffix(got.HTML, "\"") || strings.Contains(got.HTML, "data-x=\""+strings.Repeat("a", 10)) {
		t.Errorf("expected the incomplete trailing <div data-x=...> tag to be dropped, got a suffix of: %q", got.HTML[len(got.HTML)-40:])
	}
	if !strings.HasSuffix(got.HTML, "<p>hello</p>") {
		t.Errorf("expected truncation to back up to the last COMPLETE tag boundary, got: %q", got.HTML)
	}
}

// TestFetcher_Fetch_TruncatesEvenWayOverMaxBytes replaces what used to be a
// hard-ceiling test (Phase 2.1 removed the hard ceiling entirely — see
// Fetch's own doc comment: implementing "fail fast past a ceiling" meant
// reading past maxBytes to find out, which made that path slower and more
// memory-hungry than just truncating). A body 20x over the cap still only
// ever costs maxBytes+1 bytes read and truncates exactly like a body just
// barely over it — there is no separate "too big even to truncate" case
// anymore.
func TestFetcher_Fetch_TruncatesEvenWayOverMaxBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>" + strings.Repeat("x", 20_000) + "</html>"))
	}))
	defer srv.Close()

	f := newFetcherWithDialContext(unguardedDialContext)
	got, err := f.Fetch(context.Background(), srv.URL, 1000)
	if err != nil {
		t.Fatalf("expected truncation, not an error, for a 20,000-byte body against a 1000-byte cap: %v", err)
	}
	if !got.Truncated {
		t.Error("expected Truncated true")
	}
	if len(got.HTML) > 1000 {
		t.Errorf("expected at most 1000 bytes, got %d", len(got.HTML))
	}
}

func TestFetcher_Fetch_FollowsRedirects(t *testing.T) {
	const finalBody = "<html>after redirect</html>"
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(finalBody))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	f := newFetcherWithDialContext(unguardedDialContext)
	got, err := f.Fetch(context.Background(), srv.URL+"/start", 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.HTML != finalBody {
		t.Errorf("got %q, want %q", got.HTML, finalBody)
	}
}

func TestFetcher_Fetch_InvalidURLNeverDials(t *testing.T) {
	f := newFetcherWithDialContext(func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("dial should never be reached for an invalid URL")
		return nil, nil
	})
	_, err := f.Fetch(context.Background(), "not a url", 1024)
	if !errors.Is(err, ErrInvalidURL) {
		t.Fatalf("expected ErrInvalidURL, got: %v", err)
	}
}

// TestFetcher_Fetch_RetriesOnceOn5xx confirms a single 500 followed by a
// success is retried once and the eventual success is returned — not the
// first failure.
func TestFetcher_Fetch_RetriesOnceOn5xx(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte("<html>ok on retry</html>"))
	}))
	defer srv.Close()

	f := newFetcherWithDialContext(unguardedDialContext)
	got, err := f.Fetch(context.Background(), srv.URL, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requests != 2 {
		t.Errorf("expected exactly 2 requests (1 original + 1 retry), got %d", requests)
	}
	if got.HTML != "<html>ok on retry</html>" {
		t.Errorf("expected the retry's successful body, got %q", got.HTML)
	}
}

// TestFetcher_Fetch_RetriesOnceOn429ThenSucceeds mirrors the 5xx case for
// 429 specifically, since it's the one 4xx status that IS retried.
func TestFetcher_Fetch_RetriesOnceOn429ThenSucceeds(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte("<html>ok on retry</html>"))
	}))
	defer srv.Close()

	f := newFetcherWithDialContext(unguardedDialContext)
	got, err := f.Fetch(context.Background(), srv.URL, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requests != 2 {
		t.Errorf("expected exactly 2 requests, got %d", requests)
	}
	if got.HTML != "<html>ok on retry</html>" {
		t.Errorf("expected the retry's successful body, got %q", got.HTML)
	}
}

// TestFetcher_Fetch_NoRetryOn404 confirms a plain 404 is hit exactly once —
// a 4xx other than 429 describes something about the resource a retry
// can't fix.
func TestFetcher_Fetch_NoRetryOn404(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	f := newFetcherWithDialContext(unguardedDialContext)
	_, err := f.Fetch(context.Background(), srv.URL, 1024)
	if !errors.Is(err, ErrFetchFailed) {
		t.Fatalf("expected ErrFetchFailed, got: %v", err)
	}
	if requests != 1 {
		t.Errorf("expected exactly 1 request (no retry for a 404), got %d", requests)
	}
}

// statefulFailOnceDialer fails the first dial attempt with a transport
// error, then dials normally on every attempt after — used to prove Fetch
// retries a transport-level failure (not just a bad status code) exactly
// once.
type statefulFailOnceDialer struct {
	attempts int
}

func (d *statefulFailOnceDialer) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	d.attempts++
	if d.attempts == 1 {
		return nil, errors.New("simulated transport failure")
	}
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

func TestFetcher_Fetch_RetriesOnceOnTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>ok on retry</html>"))
	}))
	defer srv.Close()

	d := &statefulFailOnceDialer{}
	f := newFetcherWithDialContext(d.dial)
	got, err := f.Fetch(context.Background(), srv.URL, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.attempts != 2 {
		t.Errorf("expected exactly 2 dial attempts (1 original + 1 retry), got %d", d.attempts)
	}
	if got.HTML != "<html>ok on retry</html>" {
		t.Errorf("expected the retry's successful body, got %q", got.HTML)
	}
}

// TestFetcher_Fetch_ErrBlockedFor403 confirms a 403 — never retried (see
// TestFetcher_Fetch_NoRetryOn404's own reasoning; 403 isn't 429 either) —
// maps to the distinct ErrBlocked sentinel rather than the generic
// ErrFetchFailed.
func TestFetcher_Fetch_ErrBlockedFor403(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	f := newFetcherWithDialContext(unguardedDialContext)
	_, err := f.Fetch(context.Background(), srv.URL, 1024)
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("expected ErrBlocked for a 403, got: %v", err)
	}
	if requests != 1 {
		t.Errorf("expected exactly 1 request (no retry for a 403), got %d", requests)
	}
}

// TestFetcher_Fetch_ErrBlockedFor429AfterRetry confirms a 429 that's STILL
// a 429 after its one retry lands on ErrBlocked, not ErrFetchFailed —
// unlike TestFetcher_Fetch_RetriesOnceOn429ThenSucceeds, this one never
// recovers.
func TestFetcher_Fetch_ErrBlockedFor429AfterRetry(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	f := newFetcherWithDialContext(unguardedDialContext)
	_, err := f.Fetch(context.Background(), srv.URL, 1024)
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("expected ErrBlocked for a 429 that persists after retry, got: %v", err)
	}
	if requests != 2 {
		t.Errorf("expected exactly 2 requests (429 is retried once), got %d", requests)
	}
}

// TestFetcher_Fetch_SniffsEmptyContentTypeAsHTML confirms an empty/missing
// Content-Type with a body that genuinely opens like markup is still
// accepted — via body sniffing, not the header (which says nothing here).
func TestFetcher_Fetch_SniffsEmptyContentTypeAsHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set(..., "") rather than Del — see TestFetcher_Fetch_AllowsMissingContentType's
		// own comment: Del lets net/http sniff this doctype-opening body
		// itself and send a real "text/html" header, which would bypass the
		// empty-header/body-sniff branch this test means to exercise.
		w.Header().Set("Content-Type", "")
		w.Write([]byte("<!doctype html><html><body>hi</body></html>"))
	}))
	defer srv.Close()

	f := newFetcherWithDialContext(unguardedDialContext)
	if _, err := f.Fetch(context.Background(), srv.URL, 1024); err != nil {
		t.Fatalf("expected a doctype-opening body with no content-type header to sniff as HTML, got: %v", err)
	}
}

// TestFetcher_Fetch_RejectsEmptyContentTypeNonHTMLBody is
// SniffsEmptyContentTypeAsHTML's negative counterpart: an empty
// Content-Type on a body that does NOT open like markup must still be
// rejected — the old version of this check let anything through once the
// header was empty, regardless of the body.
func TestFetcher_Fetch_RejectsEmptyContentTypeNonHTMLBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set(..., "") rather than Del: this body doesn't open with a markup
		// tag, so net/http's own sniffing (which would run if Del left the
		// header key absent) lands on some non-html/xml type here too and
		// this particular test's outcome doesn't actually depend on the
		// distinction — kept consistent with the other two Content-Type
		// tests in this file anyway, so a reader doesn't wonder why only
		// this one uses Del, and nobody "simplifies" the other two back to
		// it by copying this one.
		w.Header().Set("Content-Type", "")
		w.Write([]byte("%PDF-1.4 this is not markup at all, just plain bytes"))
	}))
	defer srv.Close()

	f := newFetcherWithDialContext(unguardedDialContext)
	_, err := f.Fetch(context.Background(), srv.URL, 1024)
	if !errors.Is(err, ErrNotHTML) {
		t.Fatalf("expected ErrNotHTML for a non-markup body with no content-type header, got: %v", err)
	}
}

// TestFetcher_Fetch_RejectsNonHTMLWithoutReadingWholeBody is the latency
// guard for the sniff-before-full-read fix: a large non-HTML/misleadingly-
// typed body must be rejected after roughly sniffBytes, not after the
// whole thing is downloaded. Before this fix, looksLikeHTML needed the full
// body to classify anything, so a multi-megabyte PDF/video served with no
// (or a wrong) Content-Type was fully read over the wire before Fetch
// rejected it — slow, and a real memory cost for something that should
// fail almost immediately.
//
// Proven with a blocked handler, not a byte count or a wall-clock bound: an
// earlier version of this test tried to assert on bytes written before the
// server noticed the client was gone, but TCP send buffers can silently
// absorb megabytes into the kernel before a close/RST actually propagates
// back to an io.Writer.Write call on loopback — that made the byte count
// meaningless as a signal on a fast connection, not just noisy. A later
// version paced the server's writes and asserted on elapsed wall-clock
// time against a generous margin — not flaky, but still a threshold
// judgement, and it cost real seconds in the suite. This version instead
// writes exactly one sniff-sized chunk, flushes it, and then blocks the
// handler on a channel the test only closes AFTER Fetch has returned. If
// Fetch correctly bails right after sniffing, it never asks the connection
// for more, returns immediately, and the test's close(release) lets the
// handler exit cleanly. If a regression ever made Fetch read past the
// sniffed chunk, that read has nothing more to consume — the handler is
// parked on the channel, not writing — so it blocks until Fetch's own
// fetchTimeout gives up on the request; the test then fails on the
// resulting error being something other than ErrNotHTML, not on a duration
// check. Either way there's no timing assertion in this test itself.
func TestFetcher_Fetch_RejectsNonHTMLWithoutReadingWholeBody(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(bytes.Repeat([]byte{0xFF}, sniffBytes)) // binary, never sniffs as HTML
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// select on r.Context().Done() too, not just release, as a backstop:
		// release is always closed right after Fetch returns below, but if
		// Fetch itself never returns (the very regression this test exists
		// to catch, should its own fetchTimeout somehow not apply), the
		// deferred srv.Close() tearing down the connection at least gives
		// this handler goroutine a second way to unblock and exit.
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	f := newFetcherWithDialContext(unguardedDialContext)
	_, err := f.Fetch(context.Background(), srv.URL, 1024)
	close(release)

	if !errors.Is(err, ErrNotHTML) {
		t.Fatalf("expected ErrNotHTML, got: %v — a non-ErrNotHTML error here (e.g. a fetchTimeout expiry) means "+
			"Fetch asked the connection for more than the sniffed chunk", err)
	}
}

// TestFetcher_Fetch_RejectsTextPlainThatIsNotMarkup confirms text/plain is
// no longer trusted on the header alone (see looksLikeHTML's own doc
// comment) — a genuinely non-markup text/plain body must still be
// rejected.
func TestFetcher_Fetch_RejectsTextPlainThatIsNotMarkup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("just some plain text, not a webpage"))
	}))
	defer srv.Close()

	f := newFetcherWithDialContext(unguardedDialContext)
	_, err := f.Fetch(context.Background(), srv.URL, 1024)
	if !errors.Is(err, ErrNotHTML) {
		t.Fatalf("expected ErrNotHTML for a text/plain body that doesn't sniff as markup, got: %v", err)
	}
}

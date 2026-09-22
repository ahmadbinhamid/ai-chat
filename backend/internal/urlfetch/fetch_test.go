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

// unguardedDialContext skips the IsBlockedIP check so tests can hit an
// httptest.Server (always loopback) and exercise Fetch's own logic — the
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

// TestFetcher_BlocksResolvedHostname covers a HOSTNAME ("localhost") rather
// than an IP literal, proving the guard catches a blocked address reached
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
// sub-budgets are actually wired into NewFetcher's Transport.
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
		// Set(..., "") not Del: Del would leave the key absent, triggering
		// net/http's own auto-sniffing, which would send a real "text/html"
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
// instead of failing, since a merchant doesn't control a link's size.
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
// never ends mid-tag — the cut point lands inside a long attribute value
func TestFetcher_Fetch_TruncatesAtTagBoundary(t *testing.T) {
	prefix := "<html><body><p>hello</p><div data-x=\""
	// Pad well past 1000 bytes so the cut point genuinely lands mid-attribute.
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

// TestFetcher_Fetch_TruncatesEvenWayOverMaxBytes: a body 20x over the cap
// still only costs maxBytes+1 bytes read and truncates like one barely over —
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

// TestFetcher_Fetch_RetriesOnceOn5xx confirms a 500 then success returns
// the eventual success, not the first failure.
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
// 429, the one 4xx status that IS retried.
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
// a retry can't fix what a 4xx other than 429 describes.
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

// statefulFailOnceDialer fails the first dial, then dials normally after —
// proves Fetch retries a transport-level failure, not just a bad status code.
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

// TestFetcher_Fetch_ErrBlockedFor403 confirms a 403 (never retried) maps to
// the distinct ErrBlocked sentinel, not the generic ErrFetchFailed.
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

// TestFetcher_Fetch_ErrBlockedFor429AfterRetry confirms a 429 still 429
// after its one retry lands on ErrBlocked, not ErrFetchFailed.
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
func TestFetcher_Fetch_SniffsEmptyContentTypeAsHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set(..., "") not Del — see TestFetcher_Fetch_AllowsMissingContentType.
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
func TestFetcher_Fetch_RejectsEmptyContentTypeNonHTMLBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set(..., "") kept consistent with the other Content-Type tests in this file.
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

// TestFetcher_Fetch_RejectsNonHTMLWithoutReadingWholeBody confirms a large
// mislabeled body is rejected after ~sniffBytes, not after full download.
func TestFetcher_Fetch_RejectsNonHTMLWithoutReadingWholeBody(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(bytes.Repeat([]byte{0xFF}, sniffBytes)) // binary, never sniffs as HTML
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// r.Context().Done() is a backstop: if Fetch never returns (the
		// regression this test catches), srv.Close() still unblocks the handler.
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
// not trusted on the header alone (see looksLikeHTML).
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

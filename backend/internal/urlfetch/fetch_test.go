package urlfetch

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	if got != body {
		t.Errorf("got %q, want %q", got, body)
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
		w.Header().Del("Content-Type")
		w.Write([]byte("<html></html>"))
	}))
	defer srv.Close()

	f := newFetcherWithDialContext(unguardedDialContext)
	if _, err := f.Fetch(context.Background(), srv.URL, 1024); err != nil {
		t.Fatalf("unexpected error for a response with no content-type header: %v", err)
	}
}

func TestFetcher_Fetch_RejectsOverMaxBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", 2000)))
	}))
	defer srv.Close()

	f := newFetcherWithDialContext(unguardedDialContext)
	_, err := f.Fetch(context.Background(), srv.URL, 1000)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge for a 2000-byte body against a 1000-byte cap, got: %v", err)
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
	if got != finalBody {
		t.Errorf("got %q, want %q", got, finalBody)
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

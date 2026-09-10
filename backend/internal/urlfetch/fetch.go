package urlfetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// fetchTimeout bounds the whole fetch — connect, TLS handshake, headers,
// and body read combined. Generous enough for a real, slow-but-legitimate
// website; short enough that one merchant pasting an unreachable link can
// never tie up a generation for long. Not exported/configurable: matches
// this package's sibling limits (themebuild.MaxImageAttachmentBytes etc.)
// in being a fixed constant, not a per-deployment env var — nothing about
// it is environment-specific.
const fetchTimeout = 10 * time.Second

// userAgent identifies these requests as this feature's own, rather than
// the Go standard library's default ("Go-http-client/1.1") — some sites
// block or rate-limit the default UA, and it makes it possible to tell this
// deliberate reference-fetch traffic apart from anything else in a remote
// server's own access log.
const userAgent = "FlowPOS-AIThemeBuilder/1.0 (+reference-link-fetch)"

// maxRedirects bounds how many redirect hops Fetch follows before giving
// up — real sites rarely chain more than one or two (http->https, a
// trailing-slash/www redirect); this is generous headroom against that
// while still bounding the total number of DNS resolutions and IsBlockedIP
// checks one Fetch call can trigger.
const maxRedirects = 5

// Sentinel errors — wrapped with more detail via fmt.Errorf's %w in Fetch,
// so a caller can both errors.Is-match the category and log/report the
// full message. Deliberately merchant-readable on their own (no internal
// detail to redact) since themebuild.Service.Generate surfaces Fetch's
// error text straight into its own error, the same way it already does for
// ErrHTMLAttachmentTooLarge and friends.
var (
	ErrInvalidURL  = errors.New("invalid reference url")
	ErrBlockedHost = errors.New("that url points at a private or internal address and can't be used as a reference")
	ErrFetchFailed = errors.New("could not reach that url")
	ErrNotHTML     = errors.New("that url did not return a webpage")
	ErrTooLarge    = errors.New("that page is too large to use as a reference")
)

// Fetcher fetches a merchant-supplied URL's HTML — see the package doc
// comment for the SSRF threat model this guards against. Safe for
// concurrent use (http.Client is): themebuild.Service holds one shared
// instance across every tenant's generation, the same way it holds one
// shared *ai.Generator.
type Fetcher struct {
	client *http.Client
}

// NewFetcher builds a Fetcher whose every connection is guarded by
// IsBlockedIP at dial time — see guardedDialContext for why that has to
// happen at the dial, not just once against the URL's own host string.
func NewFetcher() *Fetcher {
	return newFetcherWithDialContext(guardedDialContext)
}

// newFetcherWithDialContext is NewFetcher with the dial function replaced —
// unexported, used only by this package's own tests (see fetch_test.go) to
// exercise Fetch's real logic (headers, status/content-type/size handling,
// redirects) against an httptest.Server, whose address is always loopback
// and would otherwise always be rejected by guardedDialContext itself —
// correctly; TestFetcher_BlocksLoopback confirms that exact rejection
// through the real, unmodified NewFetcher instead.
func newFetcherWithDialContext(dial func(ctx context.Context, network, addr string) (net.Conn, error)) *Fetcher {
	return &Fetcher{
		client: &http.Client{
			Transport: &http.Transport{DialContext: dial},
			Timeout:   fetchTimeout,
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) >= maxRedirects {
					return fmt.Errorf("too many redirects (max %d)", maxRedirects)
				}
				// No IsBlockedIP re-check needed here: a followed redirect
				// is just another request through the same Transport, so
				// it goes through the dial function again on its own —
				// this callback only needs to bound the hop count.
				return nil
			},
		},
	}
}

// guardedDialContext replaces http.Transport's default dial behavior: where
// the standard dialer would resolve addr's hostname and connect to
// whichever IP it gets back with no further checks, this resolves it
// itself, rejects the whole connection if ANY resolved IP is blocked (see
// IsBlockedIP), and only then dials one of the now-validated IPs directly —
// closing the DNS-rebinding gap a check made only once, earlier, against
// the URL string would leave open (a hostname that resolves safely at
// validation time but is repointed at an internal address by the time the
// connection is actually made). Applied to every dial this Fetcher's
// http.Client ever makes, including every redirect hop, with no separate
// handling needed for those.
func guardedDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}

	var ips []net.IP
	if literal := net.ParseIP(host); literal != nil {
		ips = []net.IP{literal}
	} else {
		resolved, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", host, err)
		}
		ips = resolved
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%q did not resolve to any address", host)
	}
	for _, ip := range ips {
		if IsBlockedIP(ip) {
			return nil, fmt.Errorf("%w: %s resolves to %s", ErrBlockedHost, host, ip)
		}
	}

	// Dial the specific, already-validated IP — not addr (the hostname)
	// again, which would trigger a second, unguarded resolution inside the
	// standard dialer and reopen exactly the race this function exists to
	// close.
	dialer := &net.Dialer{}
	return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
}

// Fetch validates rawURL, then performs a single SSRF-guarded GET (see the
// package doc comment), returning at most maxBytes of the response body as
// a string. The caller is expected to run the result through the same
// sanitization/size checks an uploaded HTML file already gets (see
// themebuild.SanitizeHTMLAttachment) — Fetch's own maxBytes cap exists only
// to bound how much this function itself reads off the wire before that
// happens, not as a replacement for it.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string, maxBytes int64) (string, error) {
	u, err := ValidateURL(rawURL)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidURL, err)
	}

	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidURL, err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrFetchFailed, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("%w: unexpected status %d", ErrFetchFailed, resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !looksLikeHTML(ct) {
		return "", fmt.Errorf("%w: content-type %q", ErrNotHTML, ct)
	}

	// maxBytes+1: reading one byte past the limit is what distinguishes "the
	// body was exactly maxBytes" from "the body was truncated" without
	// buffering the whole (possibly much larger) real body first.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrFetchFailed, err)
	}
	if int64(len(body)) > maxBytes {
		return "", fmt.Errorf("%w: over %d bytes", ErrTooLarge, maxBytes)
	}
	return string(body), nil
}

func looksLikeHTML(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.Contains(ct, "html") || strings.Contains(ct, "text/plain") || strings.Contains(ct, "xml")
}

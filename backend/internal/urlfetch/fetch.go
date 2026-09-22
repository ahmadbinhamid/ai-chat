package urlfetch

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// fetchTimeout bounds the whole fetch (connect+TLS+headers+body) so one
// unreachable link can't tie up a generation for long. Fixed, not configurable.
const fetchTimeout = 10 * time.Second

// dialTimeout bounds just the TCP connect step — without it, a dead host's
// own OS-level connect timeout could eat most of fetchTimeout's budget alone.
const dialTimeout = 4 * time.Second

// userAgent identifies this feature's traffic distinctly from Go's default UA
// (some sites block/rate-limit that), sent alongside browser-shaped Accept headers.
const userAgent = "FlowPOS-AIThemeBuilder/1.0 (+reference-link-fetch)"

// retryDelay is the pause before Fetch's one allowed retry — fits within
// fetchTimeout's budget while giving a transient failure time to clear.
const retryDelay = 500 * time.Millisecond

// sniffBytes bounds how much of the body looksLikeHTML inspects when
// Content-Type alone doesn't settle it.
const sniffBytes = 512

// maxRedirects bounds hop count — generous headroom above what real sites
// chain, while still bounding DNS/IsBlockedIP checks per Fetch call.
const maxRedirects = 5

// Sentinel errors, wrapped with more detail via fmt.Errorf's %w in Fetch.
// Deliberately merchant-readable: themebuild.Service.Generate surfaces this
// text straight into its own error.
var (
	ErrInvalidURL  = errors.New("invalid reference url")
	ErrBlockedHost = errors.New("that url points at a private or internal address and can't be used as a reference")
	ErrFetchFailed = errors.New("could not reach that url")
	ErrNotHTML     = errors.New("that url did not return a webpage")
	// ErrBlocked (401/403/429 after retry) is distinct from ErrFetchFailed: the
	// site is actively refusing automation, not unreachable — the merchant
	// can work around it by pasting HTML directly instead of a link.
	ErrBlocked = errors.New("that site refused an automated request — try pasting the page's HTML as a file instead")
)

// Fetcher fetches a merchant-supplied URL's HTML under the package's SSRF
// guard. Safe for concurrent use; themebuild.Service holds one shared instance.
type Fetcher struct {
	client *http.Client
}

// NewFetcher builds a Fetcher whose every connection is guarded by
// IsBlockedIP at dial time (see guardedDialer).
func NewFetcher() *Fetcher {
	return newFetcherWithDialContext(guardedDialer().DialContext)
}

// newFetcherWithDialContext is NewFetcher with the dial function swapped —
// lets tests hit an httptest.Server (always loopback) without the real guard
// rejecting it; TestFetcher_BlocksLoopback covers the real guard separately.
func newFetcherWithDialContext(dial func(ctx context.Context, network, addr string) (net.Conn, error)) *Fetcher {
	return &Fetcher{
		client: &http.Client{
			Transport: &http.Transport{
				DialContext: dial,
				// Sub-budgets within fetchTimeout so a stalled TLS handshake or
				// a connection that never sends headers can't each eat the whole 10s.
				TLSHandshakeTimeout:   4 * time.Second,
				ResponseHeaderTimeout: 6 * time.Second,
			},
			Timeout: fetchTimeout,
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) >= maxRedirects {
					return fmt.Errorf("too many redirects (max %d)", maxRedirects)
				}
				// No IsBlockedIP re-check needed: a redirect goes through the
				// same Transport, so it hits the dial guard again on its own.
				return nil
			},
		},
	}
}

// guardedDialer builds the *net.Dialer every fetch (and every redirect hop)
// connects through. The guard runs in Control, not a pre-flight check
// against the URL's hostname: Control fires after resolution, on the exact
// IP about to be dialed, closing the DNS-rebinding gap a hostname-only check
// would leave open. Letting the standard dialer resolve (rather than dialing
// only the first address ourselves) also preserves Go's normal multi-address
// fallback/Happy Eyeballs — Control still vets every address tried.
func guardedDialer() *net.Dialer {
	return &net.Dialer{
		Timeout: dialTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil || IsBlockedIP(ip) {
				// host (the resolved internal address) is never surfaced to a
				// merchant — callers only errors.Is-match this against ErrBlocked
				// and log it server-side; re-verify that if a new call site is added.
				return fmt.Errorf("%w: %s", ErrBlockedHost, host)
			}
			return nil
		},
	}
}

// Result is what Fetch returns on success. Callers should treat Truncated
// as "real but incomplete," not silently work from it as the whole page.
type Result struct {
	HTML      string
	Truncated bool
	// FinalURL is the response's URL AFTER redirects, not rawURL as passed in —
	// needed to correctly resolve relative hrefs found in the fetched HTML.
	// Never nil on a successful Fetch.
	FinalURL *url.URL
}

// Fetch validates rawURL, does an SSRF-guarded GET with one retry on a
// transient failure, and returns at most maxBytes of the body. Going over
// maxBytes truncates (at a tag boundary) rather than failing — a merchant
// doesn't control a link's size the way they control an upload's. No
// separate hard ceiling above maxBytes: reading past it just to fail fast
// would be slower and more memory-hungry than truncating. A successful
// result still needs the same sanitization an uploaded HTML file gets
// (themebuild.SanitizeHTMLAttachment) — this cap only bounds what Fetch itself reads.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string, maxBytes int64) (Result, error) {
	u, err := ValidateURL(rawURL)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrInvalidURL, err)
	}

	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	resp, err := f.doWithRetry(ctx, u.String())
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrFetchFailed, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if isBlockedStatus(resp.StatusCode) {
		return Result{}, fmt.Errorf("%w: status %d", ErrBlocked, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("%w: unexpected status %d", ErrFetchFailed, resp.StatusCode)
	}

	// Peek (not consume) sniffBytes so a mislabeled non-HTML response is
	// rejected after ~512 bytes instead of downloading the whole thing.
	br := bufio.NewReaderSize(resp.Body, sniffBytes)
	peeked, err := br.Peek(sniffBytes)
	if err != nil && !errors.Is(err, io.EOF) {
		// EOF before sniffBytes just means a short body; any other error is real.
		return Result{}, fmt.Errorf("%w: %w", ErrFetchFailed, err)
	}
	if !looksLikeHTML(resp.Header.Get("Content-Type"), peeked) {
		return Result{}, fmt.Errorf("%w: content-type %q", ErrNotHTML, resp.Header.Get("Content-Type"))
	}

	// Read maxBytes+1: the extra byte is what distinguishes "exactly maxBytes"
	// from "truncated" without buffering the whole real body first.
	body, err := io.ReadAll(io.LimitReader(br, maxBytes+1))
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrFetchFailed, err)
	}
	if int64(len(body)) > maxBytes {
		return Result{HTML: TruncateAtTagBoundary(string(body), maxBytes), Truncated: true, FinalURL: resp.Request.URL}, nil
	}
	return Result{HTML: string(body), FinalURL: resp.Request.URL}, nil
}

// doWithRetry retries exactly once on the SAME ctx as the first attempt
// (already bounded to fetchTimeout) — a failing endpoint can't double its
// time budget just by being retryable.
func (f *Fetcher) doWithRetry(ctx context.Context, url string) (*http.Response, error) {
	resp, err := f.do(ctx, url)
	if !shouldRetry(resp, err) {
		return resp, err
	}
	if resp != nil {
		_ = resp.Body.Close()
	}
	select {
	case <-time.After(retryDelay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return f.do(ctx, url)
}

// shouldRetry is true for a transport-level error or a retryable status
// (see isRetryableStatus) — never for any other 4xx, which a delay can't fix.
func shouldRetry(resp *http.Response, err error) bool {
	if err != nil {
		return true
	}
	return isRetryableStatus(resp.StatusCode)
}

// isRetryableStatus: transient server error (5xx) or rate-limiting (429).
func isRetryableStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || statusCode >= 500
}

// isBlockedStatus is checked after the retry has run its course: still-429,
// or a never-retried 401/403, means the site is actively refusing
// automation, not merely unreachable — hence the distinct ErrBlocked sentinel.
func isBlockedStatus(statusCode int) bool {
	return statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden || statusCode == http.StatusTooManyRequests
}

// do issues one GET with this Fetcher's standard headers. Split out from
// Fetch/doWithRetry so the retry is byte-for-byte the same request.
func (f *Fetcher) do(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	// Browser-shaped Accept/Accept-Language: bot protection (Cloudflare/Akamai)
	// scores a bare "Accept: text/html" with no Accept-Language as suspicious.
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	return f.client.Do(req)
}

// TruncateAtTagBoundary cuts html to at most maxBytes, backing up to the
// last '<' only when the cut genuinely lands inside an unfinished tag —
// handing the model/sanitizer a half-written tag reads as broken markup,
// not as "the page continues past here."
func TruncateAtTagBoundary(html string, maxBytes int64) string {
	if maxBytes <= 0 || int64(len(html)) <= maxBytes {
		return html
	}
	cut := html[:maxBytes]
	lastOpen := strings.LastIndexByte(cut, '<')
	if lastOpen < 0 {
		return trimIncompleteTrailingRune(cut)
	}
	if strings.IndexByte(cut[lastOpen:], '>') >= 0 {
		return trimIncompleteTrailingRune(cut)
	}
	// cut[:lastOpen] ends right before a '<' (single-byte ASCII), so it can
	// never be split mid-rune — no boundary check needed on this path.
	return cut[:lastOpen]
}

// trimIncompleteTrailingRune backs s up to the last valid UTF-8 boundary when
// a byte-index cut landed mid-character — independent of tag-boundary
// truncation above. O(1): checks only the last few bytes (UTF-8's max width).
func trimIncompleteTrailingRune(s string) string {
	for i := 0; i < utf8.UTFMax && i < len(s); i++ {
		start := len(s) - 1 - i
		if utf8.RuneStart(s[start]) {
			if utf8.FullRuneInString(s[start:]) {
				return s
			}
			return s[:start]
		}
	}
	return s
}

// looksLikeHTML trusts contentType only when it explicitly says html/xml —
// text/plain is deliberately NOT trusted on the header alone, since hostile
// or misconfigured endpoints commonly serve arbitrary content as text/plain.
// Anything else falls through to sniffing the body's opening bytes.
func looksLikeHTML(contentType string, body []byte) bool {
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "html") || strings.Contains(ct, "xml") {
		return true
	}
	return sniffsAsHTML(body)
}

// sniffsAsHTML checks the first sniffBytes for a real markup opening
// ("<!doctype html"/"<html"/"<!--", or '<' followed by a letter) — a stray
// '<' elsewhere in a non-markup body doesn't count.
func sniffsAsHTML(body []byte) bool {
	if len(body) > sniffBytes {
		body = body[:sniffBytes]
	}
	s := strings.TrimSpace(strings.ToLower(string(body)))
	if strings.HasPrefix(s, "<!doctype html") || strings.HasPrefix(s, "<html") || strings.HasPrefix(s, "<!--") {
		return true
	}
	return len(s) >= 2 && s[0] == '<' && s[1] >= 'a' && s[1] <= 'z'
}

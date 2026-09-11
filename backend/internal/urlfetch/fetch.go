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

// fetchTimeout bounds the whole fetch — connect, TLS handshake, headers,
// and body read combined. Generous enough for a real, slow-but-legitimate
// website; short enough that one merchant pasting an unreachable link can
// never tie up a generation for long. Not exported/configurable: matches
// this package's sibling limits (themebuild.MaxImageAttachmentBytes etc.)
// in being a fixed constant, not a per-deployment env var — nothing about
// it is environment-specific.
const fetchTimeout = 10 * time.Second

// dialTimeout bounds the TCP connect step alone, not the whole fetch (see
// fetchTimeout for that). A dead or unreachable host's own OS-level connect
// timeout can run far longer than this on its own; without a tighter bound
// here, one bad address could consume most or all of fetchTimeout's budget
// just connecting, leaving nothing for TLS/headers/body on a retry.
const dialTimeout = 4 * time.Second

// userAgent identifies these requests as this feature's own, rather than
// the Go standard library's default ("Go-http-client/1.1") — some sites
// block or rate-limit the default UA, and it makes it possible to tell this
// deliberate reference-fetch traffic apart from anything else in a remote
// server's own access log. Sent alongside a browser-shaped Accept/
// Accept-Language pair (see the request-building code below) — the point
// of those two headers is a plausible, ordinary-looking request, not an
// anonymous one; the UA stays honestly identifying.
const userAgent = "FlowPOS-AIThemeBuilder/1.0 (+reference-link-fetch)"

// retryDelay is the single fixed pause before Fetch's one allowed retry
// (see shouldRetry) — short enough to still fit inside fetchTimeout's 10s
// budget alongside a real attempt on each side of it, long enough to
// plausibly land after whatever transient condition (a momentary 5xx, a
// rate limiter's own window) triggered the retry in the first place.
const retryDelay = 500 * time.Millisecond

// sniffBytes bounds how much of the body looksLikeHTML inspects when the
// Content-Type header alone doesn't settle it — enough to see past a
// <!doctype>/<html> opening tag and any leading whitespace/BOM a real page
// might have, without needing to read further just to classify it.
const sniffBytes = 512

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
	// ErrBlocked is distinct from ErrFetchFailed: a 401/403/429 (after the
	// one retry — see shouldRetry) means the site itself is actively
	// refusing an automated request, not that it's unreachable. There's a
	// concrete, different thing a merchant can do about this one — paste
	// the page's HTML as a file instead of a link — so it gets its own
	// sentinel and its own merchant-readable text rather than folding into
	// ErrFetchFailed's generic "could not reach it."
	ErrBlocked = errors.New("that site refused an automated request — try pasting the page's HTML as a file instead")
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
// IsBlockedIP at dial time — see guardedDialer for why that has to happen
// via Control, at the dial, not just once against the URL's own host
// string.
func NewFetcher() *Fetcher {
	return newFetcherWithDialContext(guardedDialer().DialContext)
}

// newFetcherWithDialContext is NewFetcher with the dial function replaced —
// unexported, used only by this package's own tests (see fetch_test.go) to
// exercise Fetch's real logic (headers, status/content-type/size handling,
// redirects) against an httptest.Server, whose address is always loopback
// and would otherwise always be rejected by guardedDialer's Control hook —
// correctly; TestFetcher_BlocksLoopback confirms that exact rejection
// through the real, unmodified NewFetcher instead.
func newFetcherWithDialContext(dial func(ctx context.Context, network, addr string) (net.Conn, error)) *Fetcher {
	return &Fetcher{
		client: &http.Client{
			Transport: &http.Transport{
				DialContext: dial,
				// Sub-budgets within the overall fetchTimeout: a dead host
				// with a TLS listener that never completes its handshake, or
				// one that accepts the connection but never sends headers,
				// would otherwise each be free to consume the whole 10s on
				// their own step. dialTimeout (see its own doc comment)
				// covers the connect step; these cover the two after it.
				TLSHandshakeTimeout:   4 * time.Second,
				ResponseHeaderTimeout: 6 * time.Second,
			},
			Timeout: fetchTimeout,
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

// guardedDialer builds the *net.Dialer every fetch (and every redirect
// hop — see newFetcherWithDialContext's CheckRedirect, which needs no
// separate handling since a followed redirect is just another request
// through the same Transport) actually connects through.
//
// The guard lives in Control, not a pre-flight check against the URL's own
// host string, and that's deliberate: Control runs after the standard
// library has already resolved the address and is about to call connect(2)
// on one specific IP, so it validates the exact address a connection is
// about to be made to. A check made once, earlier, against the hostname
// would leave the DNS-rebinding gap open — a name that resolves safely at
// validation time but is repointed at an internal address by the time the
// connection is actually made.
//
// This also removes the single-address limitation an earlier version of
// this function had: that version resolved the host itself and dialed only
// the first address back, so a host whose first resolved address happened
// to be unreachable (a published AAAA record with no working IPv6 route
// out of the container, for example — common) paid the full connect
// timeout and failed outright for a site that loads fine in a real
// browser. Letting the standard dialer do its own resolution means Go's
// normal multi-address fallback (and, for a dual-stack host, Happy
// Eyeballs) works as designed — Control is consulted for each address it
// tries in turn, so a blocked address is still rejected before any bytes
// go out on it, exactly as before.
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
				// Traced (see this fmt.Errorf, not the sentinel): host — the
				// resolved internal address — never reaches a merchant.
				// This error is wrapped by net.Dialer/net/http into a
				// *net.OpError ("dial tcp <host>:<port>: ..."), which Fetch
				// (below) wraps again under ErrFetchFailed and returns. Its
				// only call site (themebuild.Service.fetchReferenceURL,
				// consumed as `ferr` in doGenerate) never stringifies it into
				// anything merchant-facing — ferr only feeds an
				// errors.Is(ferr, ErrBlocked) check and a slog.Warn (server
				// log only). The actual merchant-facing text comes from
				// promptWithHTMLAttachment's own fixed strings, which never
				// read ferr.Error() at all. If a new call site to Fetch is
				// ever added, re-verify this before assuming it still holds —
				// Go's own "dial tcp <host>:<port>:" prefix would leak host
				// right alongside this sentinel's own wrap either way.
				return fmt.Errorf("%w: %s", ErrBlockedHost, host)
			}
			return nil
		},
	}
}

// Result is what Fetch returns on success — the fetched HTML (cut down to
// at most the requested maxBytes — see Truncated) and whether it had to be
// cut short. A caller that cares (see themebuild's use of this) should
// treat Truncated as "the page is real, but incomplete" rather than
// silently working from a partial document as if it were the whole page.
type Result struct {
	HTML      string
	Truncated bool
	// FinalURL is resp.Request.URL — the URL the response actually came
	// from, AFTER any redirects Fetch followed, not rawURL as passed in. A
	// stylesheet or asset href found in the fetched HTML is usually
	// relative, so resolving it correctly requires the document's actual
	// location, not the one originally requested (a redirect from
	// "example.com" to "www.example.com/en/" changes what a bare "style.css"
	// href resolves to). Never nil on a successful Fetch.
	FinalURL *url.URL
}

// Fetch validates rawURL, then performs an SSRF-guarded GET (see the
// package doc comment) with one retry on a transient failure (see
// shouldRetry), returning at most maxBytes of the response body. Going over
// maxBytes is not itself a failure — a link a merchant references is not
// something they control the size of the way an upload is, so Fetch
// truncates (at a tag boundary — see TruncateAtTagBoundary) instead of
// rejecting outright. There is deliberately no separate hard ceiling above
// maxBytes: an earlier version of this function had one, reasoning that a
// pathological endpoint should fail fast rather than be truncated — but
// implementing that meant reading past maxBytes to find out, which made
// the "fail fast" case slower and more memory-hungry than the truncating
// case it was trying to protect against. The read below is capped at
// maxBytes+1, exactly as it was before that ceiling existed: a genuinely
// endless stream is still bounded by fetchTimeout and by this same read
// limit, with no second mechanism needed. The caller is still expected to
// run a successful result through the same sanitization/size checks an
// uploaded HTML file already gets (see themebuild.SanitizeHTMLAttachment)
// — Fetch's own maxBytes cap bounds how much this function itself reads
// and keeps, not a replacement for that.
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

	// Latency: classify BEFORE reading the rest of the body, not after —
	// bufio.Reader.Peek looks at the first sniffBytes without consuming
	// them, so a non-HTML response (a PDF, a video, anything mislabeled or
	// unlabeled) is rejected after ~512 bytes instead of after downloading
	// the whole thing. The peeked bytes stay buffered in br and are read
	// again (not re-fetched over the wire) by the io.ReadAll below, so nothing
	// is read from the connection twice.
	br := bufio.NewReaderSize(resp.Body, sniffBytes)
	peeked, err := br.Peek(sniffBytes)
	if err != nil && !errors.Is(err, io.EOF) {
		// A short body (EOF before sniffBytes) is normal and expected —
		// peeked still holds whatever was actually there. Any other error
		// is a genuine read failure.
		return Result{}, fmt.Errorf("%w: %w", ErrFetchFailed, err)
	}
	if !looksLikeHTML(resp.Header.Get("Content-Type"), peeked) {
		return Result{}, fmt.Errorf("%w: content-type %q", ErrNotHTML, resp.Header.Get("Content-Type"))
	}

	// maxBytes+1: reading one byte past the limit is what distinguishes "the
	// body was exactly maxBytes" from "the body was truncated" without
	// buffering the whole (possibly much larger) real body first. Reads
	// from br, not resp.Body directly, so this picks up right after the
	// peeked prefix rather than re-reading it.
	body, err := io.ReadAll(io.LimitReader(br, maxBytes+1))
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrFetchFailed, err)
	}
	if int64(len(body)) > maxBytes {
		return Result{HTML: TruncateAtTagBoundary(string(body), maxBytes), Truncated: true, FinalURL: resp.Request.URL}, nil
	}
	return Result{HTML: string(body), FinalURL: resp.Request.URL}, nil
}

// doWithRetry performs the GET, retrying exactly once (see retryDelay) when
// shouldRetry says the first attempt's outcome is worth a second try. The
// retry happens on the SAME ctx as the first attempt (already bounded to
// fetchTimeout by Fetch) rather than a fresh one — a slow, ultimately-
// still-failing endpoint doesn't get to double its total time budget just
// by being retryable.
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

// shouldRetry reports whether Fetch's one allowed retry applies: a
// transport-level error (err != nil — resp is nil in that case, by
// net/http's own contract), or a retryable status (see isRetryableStatus).
// Never true for any other 4xx — a 404 or a plain 401/403 describes
// something about the request/resource itself that a fixed 500ms delay
// does not change.
func shouldRetry(resp *http.Response, err error) bool {
	if err != nil {
		return true
	}
	return isRetryableStatus(resp.StatusCode)
}

// isRetryableStatus is a transient server-side condition (5xx) or explicit
// rate-limiting (429) — see shouldRetry.
func isRetryableStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || statusCode >= 500
}

// isBlockedStatus is checked AFTER the retry has already run its course
// (shouldRetry/doWithRetry) — a 429 that's still a 429 after one retry, or
// a 401/403 (never retried — see shouldRetry) at all, means the site is
// actively refusing this as an automated request, not merely unreachable.
// See ErrBlocked's own doc comment for why that gets a distinct sentinel.
func isBlockedStatus(statusCode int) bool {
	return statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden || statusCode == http.StatusTooManyRequests
}

// do issues one GET with this Fetcher's standard headers — no retry, no
// status/body handling, just the request. Split out from Fetch/doWithRetry
// so the retry attempt is byte-for-byte the same request as the first.
func (f *Fetcher) do(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	// A browser-shaped Accept/Accept-Language pair, alongside (not instead
	// of) the identifying User-Agent above — plenty of real sites sit
	// behind bot protection (Cloudflare/Akamai) that scores a bare
	// "Accept: text/html" with no Accept-Language as suspicious on its own,
	// independent of the UA string itself.
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	return f.client.Do(req)
}

// TruncateAtTagBoundary cuts html to at most maxBytes, backing up to the
// last '<' in the retained slice IF that '<' doesn't already have a
// matching '>' within the retained slice — i.e. only when the cut point
// genuinely landed inside an unfinished tag. A cut that already lands
// cleanly (the last '<' closes before maxBytes) is left alone: backing up
// unconditionally would incorrectly discard a complete trailing tag (e.g.
// "...</p>" right at the boundary) for no reason. The model and the
// sanitizer both parse this as markup — handing either a half-written tag
// ("<div clas") reads as broken input, not "the rest of the page is
// missing," which is the actual, honest state truncation should leave
// this in.
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
	// cut[:lastOpen] always ends right before a '<', which is single-byte
	// ASCII and so can never itself be split mid-rune — no boundary check
	// needed on this path.
	return cut[:lastOpen]
}

// trimIncompleteTrailingRune backs up s to the last valid UTF-8 boundary
// when a raw byte-index cut (maxBytes above is a byte count, not a rune
// count) landed inside a multi-byte character — TruncateAtTagBoundary's
// tag-boundary logic guards against a half-written TAG, not a half-written
// RUNE, and the two are independent (a cut can land cleanly between tags
// while still landing mid-character inside one's text content). Checks
// only the last few bytes (UTF-8's longest encoding is 4 bytes), not the
// whole string, so this stays O(1) regardless of s's length.
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

// looksLikeHTML reports whether contentType or the body's own leading
// bytes indicate real markup. The header alone is trusted only when it
// explicitly says html or xml — text/plain is deliberately NOT trusted on
// the header alone (unlike an earlier version of this function): plenty of
// misconfigured or actively hostile endpoints serve arbitrary content as
// text/plain, and unlike html/xml that MIME type says nothing about the
// body's actual shape. An empty or missing header is never enough on its
// own either way — both cases fall through to sniffing the body's own
// opening bytes (see sniffsAsHTML).
func looksLikeHTML(contentType string, body []byte) bool {
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "html") || strings.Contains(ct, "xml") {
		return true
	}
	return sniffsAsHTML(body)
}

// sniffsAsHTML inspects at most the first sniffBytes of body for a real
// markup opening: an explicit "<!doctype html"/"<html"/"<!--" (a page that
// opens with a comment — an IE conditional comment, a copyright header —
// before its first real tag), or — more generally, for a page that opens
// with a different root element first — a leading '<' immediately followed
// by an ASCII letter, which only a real tag produces; a body that merely
// contains a stray '<' somewhere (a PDF, a binary blob, plain text about
// "a < b") does not open with one.
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

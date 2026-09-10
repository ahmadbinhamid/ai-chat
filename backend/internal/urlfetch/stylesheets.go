package urlfetch

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/sync/errgroup"
)

// maxStylesheets bounds how many external <link rel="stylesheet"> hrefs
// FetchStylesheets follows — in document order, so the page's own main
// stylesheet(s) (near the top of <head>, where real sites put them) win
// over a late print/RSS/alternate-language stylesheet link. Three is
// enough to catch a typical site's base + framework + theme split without
// turning one reference fetch into a long fan-out.
const maxStylesheets = 3

// maxStylesheetBytes bounds the TOTAL combined size of collected CSS —
// inline <style> content plus every fetched external stylesheet together,
// not each individually. Once this is spent, FetchStylesheets stops
// accumulating; it never fails the reference over it. 150KB of real CSS is
// far more design signal (colors, type, spacing, radii — see digest.go)
// than the page's own HTML ever was per byte, so this is deliberately a
// meaningful budget, not an afterthought.
const maxStylesheetBytes = 150 * 1024

// stylesheetPhaseTimeout bounds the WHOLE stylesheet phase — not per file,
// total — derived from the parent context via context.WithTimeout, same
// pattern as fetchTimeout bounds Fetch itself. This is genuinely one more
// round trip added to a reference turn (see FetchStylesheets' own doc
// comment on why it's still a net latency win): 3 seconds is enough for
// the concurrent fetches below to complete against real sites in the
// common case, while keeping the worst case (an unresponsive CSS host)
// small next to fetchTimeout's own 10s and negligible next to how long
// actual generation takes. CSS is an enhancement, never a reason to make a
// merchant wait — a slow or hanging stylesheet host just means less design
// signal in the digest, not a slower turn.
const stylesheetPhaseTimeout = 3 * time.Second

// FetchStylesheets extracts <link rel="stylesheet" href="..."> hrefs (and
// any <base href> that changes how they resolve) plus inline <style> block
// contents from htmlSrc, fetches the external stylesheets CONCURRENTLY
// (via errgroup — costing roughly one round trip, not maxStylesheets of
// them) through this SAME Fetcher, and returns the combined CSS text up to
// maxStylesheetBytes. finalURL (Result.FinalURL — the document's own
// resolved location, after redirects) is the base used when the document
// has no <base> of its own.
//
// Reusing this Fetcher's client means every SSRF guarantee from Fetch
// itself applies unchanged — the guardedDialer Control hook, the dial/TLS/
// header sub-timeouts, the one-retry rule — with no second HTTP client
// built for this. Deliberately NOT restricted to finalURL's own origin:
// real sites routinely serve CSS from a CDN subdomain or an entirely
// different domain (a shared design-system host, a fonts/assets CDN), and
// restricting to same-origin would silently lose most real stylesheets. It
// is safe to follow an arbitrary href found in fetched content specifically
// BECAUSE IsBlockedIP runs at dial time for every one of these requests
// (guardedDialer, same as any other fetch through this Fetcher) — that
// guard, not an origin check, is what makes this safe.
//
// A stylesheet that fails, times out, is blocked by the SSRF guard, or
// simply isn't reached before stylesheetPhaseTimeout expires is skipped
// silently — CSS is an enhancement to the eventual digest (see digest.go),
// never a reason to fail a reference whose HTML already fetched
// successfully.
//
// Returns the combined CSS text and count, the number of EXTERNAL
// stylesheets actually fetched successfully (not counting inline <style>
// content, and not counting one that failed/timed out/was blocked) — a
// caller (doGenerate) uses count for its own merchant-facing narration
// ("read N stylesheets"), so it needs to reflect genuine successes, not
// just how many hrefs were attempted.
func (f *Fetcher) FetchStylesheets(ctx context.Context, htmlSrc string, finalURL *url.URL) (css string, count int) {
	hrefs, inlineCSS := extractStylesheetSources(htmlSrc, finalURL)

	ctx, cancel := context.WithTimeout(ctx, stylesheetPhaseTimeout)
	defer cancel()

	fetched := make([]string, len(hrefs))
	ok := make([]bool, len(hrefs))
	var g errgroup.Group
	for i, href := range hrefs {
		g.Go(func() error {
			// Each individual fetch is allowed up to the full
			// maxStylesheetBytes budget on its own — the actual combined
			// cap is enforced once below, after every goroutine has
			// returned, by combineCSS. Synchronizing a single shared
			// byte counter live across concurrent in-flight downloads
			// would need real coordination for a saving that only
			// matters in the rare case of more than one large stylesheet
			// racing at once; capping and combining after the fact is
			// simpler and already bounded by stylesheetPhaseTimeout
			// either way. Any error here (network failure, SSRF block,
			// non-2xx status) is swallowed, not returned to the group —
			// see this function's own doc comment for why one
			// stylesheet's failure must never affect, let alone cancel,
			// the others.
			body, err := f.fetchRaw(ctx, href, maxStylesheetBytes)
			if err != nil {
				return nil
			}
			fetched[i] = string(body)
			ok[i] = true // tracked separately from fetched[i] != "" — an empty-but-successful CSS response is a real success, not a failure
			return nil
		})
	}
	_ = g.Wait() // never returns a non-nil error — every goroutine above always returns nil

	for _, succeeded := range ok {
		if succeeded {
			count++
		}
	}

	chunks := make([]string, 0, len(fetched)+1)
	// Inline CSS first: often the page's own critical/above-the-fold
	// styles (see this function's own doc comment), so it's the part of
	// the budget kept even when external stylesheets together would have
	// spent all of it.
	chunks = append(chunks, inlineCSS)
	chunks = append(chunks, fetched...)
	return combineCSS(maxStylesheetBytes, chunks), count
}

// combineCSS concatenates chunks in order up to budget bytes total,
// stopping (truncating the chunk that would overflow, at a valid UTF-8
// boundary) once it's spent — this is where "stop accumulating once the
// budget is spent, don't fail" (FetchStylesheets' own doc comment) is
// actually enforced, after every source's content is already in hand.
func combineCSS(budget int, chunks []string) string {
	var b strings.Builder
	for _, chunk := range chunks {
		if b.Len() >= budget {
			break
		}
		remaining := budget - b.Len()
		if len(chunk) > remaining {
			chunk = trimIncompleteTrailingRune(chunk[:remaining])
		}
		b.WriteString(chunk)
	}
	return b.String()
}

// fetchRaw is Fetch's shared plumbing — SSRF-guarded dial (via this same
// Fetcher's client), the one-retry rule, and status-code classification —
// without Fetch's HTML-specific parts (content-type/body sniffing via
// looksLikeHTML, tag-boundary-aware truncation): a stylesheet is knowingly
// not HTML, so classifying it as such would always reject it, and CSS
// truncation needs no tag-boundary awareness the way HTML does. Reads at
// most maxBytes; anything beyond that is silently dropped, matching
// FetchStylesheets' "an enhancement, never a failure reason" treatment of
// CSS — there's no equivalent of Result.Truncated here for callers to act
// on. Does not add its own fetchTimeout sub-context the way Fetch does:
// callers (FetchStylesheets) already bound ctx to stylesheetPhaseTimeout,
// which is tighter and applies to the whole phase at once.
func (f *Fetcher) fetchRaw(ctx context.Context, rawURL string, maxBytes int64) ([]byte, error) {
	u, err := ValidateURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidURL, err)
	}
	resp, err := f.doWithRetry(ctx, u.String())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrFetchFailed, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if isBlockedStatus(resp.StatusCode) {
		return nil, fmt.Errorf("%w: status %d", ErrBlocked, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: unexpected status %d", ErrFetchFailed, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBytes))
}

// extractStylesheetSources walks htmlSrc's tokens to find every stylesheet
// <link>'s href (resolved to an absolute URL, deduplicated, capped at
// maxStylesheets, in document order) and the concatenated text content of
// every inline <style> block. Two passes over the tokenizer, not one: a
// <base href> can affect how EVERY relative href in the document resolves
// regardless of where in the document it physically appears (matching how
// a browser applies it), so the base has to be known before hrefs are
// resolved — findBaseHref runs first and cheaply (it stops at the first
// <base>, which real documents put early in <head>), then the real
// extraction pass runs with a settled base. Pure function of its inputs —
// no network, table-driven tests, per CLAUDE.md rule 2.
func extractStylesheetSources(htmlSrc string, finalURL *url.URL) (hrefs []string, inlineCSS string) {
	base := finalURL
	if href, ok := findBaseHref(htmlSrc); ok {
		if resolved, ok := resolveHref(href, finalURL); ok {
			base = resolved
		}
	}

	seen := make(map[string]bool)
	var inline strings.Builder
	inStyle := false
	z := html.NewTokenizer(strings.NewReader(htmlSrc))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return hrefs, inline.String()
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			switch t.Data {
			case "link":
				if !hasRelToken(attrVal(t, "rel"), "stylesheet") {
					continue
				}
				href := attrVal(t, "href")
				if href == "" || len(hrefs) >= maxStylesheets {
					continue
				}
				resolved, ok := resolveHref(href, base)
				if !ok {
					continue
				}
				key := resolved.String()
				if seen[key] {
					continue
				}
				seen[key] = true
				hrefs = append(hrefs, key)
			case "style":
				inStyle = true
			}
		case html.EndTagToken:
			t := z.Token()
			if t.Data == "style" {
				inStyle = false
			}
		case html.TextToken:
			if inStyle {
				inline.WriteString(z.Token().Data)
			}
		}
	}
}

// findBaseHref returns the first <base href="..."> found anywhere in
// htmlSrc — a document has at most one that counts (a browser ignores any
// after the first), so this stops scanning the moment it finds one rather
// than walking the rest of a possibly-large document for nothing.
func findBaseHref(htmlSrc string) (string, bool) {
	z := html.NewTokenizer(strings.NewReader(htmlSrc))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return "", false
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			if t.Data != "base" {
				continue
			}
			if href := attrVal(t, "href"); href != "" {
				return href, true
			}
		}
	}
}

// attrVal returns t's attribute named key, or "" if absent — x/net/html
// already lower-cases attribute keys during tokenization, so key must be
// passed lowercased.
func attrVal(t html.Token, key string) string {
	for _, a := range t.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// hasRelToken reports whether rel (a space-separated attribute value, per
// the HTML spec — a real page's rel="preload stylesheet" or
// rel="stylesheet alternate" is common) contains token as one of its
// space-separated entries, case-insensitively. Matching the whole
// attribute against "stylesheet" would miss both of those.
func hasRelToken(rel, token string) bool {
	for _, part := range strings.Fields(rel) {
		if strings.EqualFold(part, token) {
			return true
		}
	}
	return false
}

// resolveHref resolves href (absolute, protocol-relative like
// "//cdn.example.com/x.css", or relative like "/x.css" or "x.css") against
// base per standard URL-reference resolution (url.URL.ResolveReference
// implements RFC 3986 §5, which is exactly what a browser does with an
// href found in a document). Returns ok=false for a genuinely unparseable
// href — a caller skips it, the same "this one enhancement source didn't
// work out" treatment as a failed fetch.
func resolveHref(href string, base *url.URL) (*url.URL, bool) {
	ref, err := url.Parse(href)
	if err != nil {
		return nil, false
	}
	if base == nil {
		if !ref.IsAbs() {
			return nil, false
		}
		return ref, true
	}
	return base.ResolveReference(ref), true
}

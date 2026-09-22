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

// maxStylesheets bounds how many <link rel="stylesheet"> hrefs
// FetchStylesheets follows, in document order — enough for a typical
const maxStylesheets = 3

// maxStylesheetBytes bounds the TOTAL combined CSS (inline + external).
// Once spent, FetchStylesheets stops accumulating rather than failing.
const maxStylesheetBytes = 150 * 1024

// maxStylesheetBytesPerFile is maxStylesheetBytes split evenly across
// maxStylesheets concurrent fetches, so they can't pull more than the
const maxStylesheetBytesPerFile = maxStylesheetBytes / maxStylesheets

// stylesheetPhaseTimeout bounds the WHOLE stylesheet phase, not per file.
// CSS is an enhancement, never a reason to make a merchant wait, so this
const stylesheetPhaseTimeout = 3 * time.Second

// FetchStylesheets extracts stylesheet <link> hrefs and inline <style>
// content from htmlSrc, fetches the external ones CONCURRENTLY through this
func (f *Fetcher) FetchStylesheets(ctx context.Context, htmlSrc string, finalURL *url.URL) (css string, count int) {
	hrefs, inlineCSS := extractStylesheetSources(htmlSrc, finalURL)

	ctx, cancel := context.WithTimeout(ctx, stylesheetPhaseTimeout)
	defer cancel()

	fetched := make([]string, len(hrefs))
	var g errgroup.Group
	for i, href := range hrefs {
		g.Go(func() error {
			// Reads only its even share (maxStylesheetBytesPerFile), not the
			// full budget — keeps maxStylesheets concurrent fetches bounded
			body, err := f.fetchRaw(ctx, href, maxStylesheetBytesPerFile)
			if err != nil {
				return nil
			}
			fetched[i] = string(body)
			return nil
		})
	}
	_ = g.Wait() // every goroutine above always returns nil

	chunks := make([]string, 0, len(fetched)+1)
	// Inline CSS first — often the page's own critical/above-the-fold styles,
	// so it's kept even when external stylesheets together spend the rest of the budget.
	chunks = append(chunks, inlineCSS)
	chunks = append(chunks, fetched...)
	combined, contributed := combineCSS(maxStylesheetBytes, chunks)
	// contributed includes inline CSS (chunks[0]); subtract it back out so
	// count reflects only external stylesheets, per this function's doc comment.
	count = contributed
	if inlineCSS != "" {
		count--
	}
	return combined, count
}

// combineCSS concatenates chunks up to budget bytes, truncating the chunk
// that overflows at a valid UTF-8 boundary. contributed counts chunks that
func combineCSS(budget int, chunks []string) (text string, contributed int) {
	var b strings.Builder
	for _, chunk := range chunks {
		if b.Len() >= budget {
			break
		}
		remaining := budget - b.Len()
		if len(chunk) > remaining {
			chunk = trimIncompleteTrailingRune(chunk[:remaining])
		}
		if len(chunk) > 0 {
			contributed++
		}
		b.WriteString(chunk)
	}
	return b.String(), contributed
}

// fetchRaw is Fetch's shared plumbing (SSRF-guarded dial, one-retry rule,
// status classification) minus the HTML-specific parts — a stylesheet isn't
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

// mediaAllowsScreen reports whether a media attribute still applies to
// ordinary screen rendering — absent/empty always does (per the HTML spec,
func mediaAllowsScreen(media string) bool {
	if media == "" {
		return true
	}
	media = strings.ToLower(media)
	return strings.Contains(media, "screen") || strings.Contains(media, "all")
}

// extractStylesheetSources collects stylesheet <link> hrefs (resolved,
// deduped, capped, in document order) and inline <style> text, skipping
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
	skipStyle := false // true for the current <style>...</style> when it's print-only or inside an <svg>
	svgDepth := 0
	z := html.NewTokenizer(strings.NewReader(htmlSrc))
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return hrefs, inline.String()
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			switch t.Data {
			case "link":
				if !hasRelToken(attrVal(t, "rel"), "stylesheet") || !mediaAllowsScreen(attrVal(t, "media")) {
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
			case "svg":
				// A self-closing <svg/> has no content, so only a real
				// StartTagToken opens a subtree worth tracking.
				if tt == html.StartTagToken {
					svgDepth++
				}
			case "style":
				inStyle = true
				skipStyle = svgDepth > 0 || !mediaAllowsScreen(attrVal(t, "media"))
			}
		case html.EndTagToken:
			t := z.Token()
			switch t.Data {
			case "style":
				inStyle = false
			case "svg":
				if svgDepth > 0 {
					svgDepth--
				}
			}
		case html.TextToken:
			if inStyle && !skipStyle {
				inline.WriteString(z.Token().Data)
			}
		}
	}
}

// findBaseHref returns the first <base href="..."> — a browser ignores any
// after the first, so this stops scanning as soon as it finds one.
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

// attrVal returns t's attribute named key, or "" if absent — key must be
// lowercase since x/net/html lower-cases attribute keys during tokenization.
func attrVal(t html.Token, key string) string {
	for _, a := range t.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// hasRelToken checks token against each space-separated entry in rel
// (e.g. rel="preload stylesheet"), case-insensitively — matching the whole
func hasRelToken(rel, token string) bool {
	for _, part := range strings.Fields(rel) {
		if strings.EqualFold(part, token) {
			return true
		}
	}
	return false
}

// resolveHref resolves href (absolute, protocol-relative, or relative)
// against base per RFC 3986 §5. Returns ok=false for an unparseable href,
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

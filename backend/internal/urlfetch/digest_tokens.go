package urlfetch

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

// maxCustomProperties bounds how many CSS custom properties (--brand-green:
// #6E9A3A) BuildDigest lists verbatim, in first-declared order — a real
// design system's :root block is usually a few dozen entries at most; this
// is generous headroom above that while still bounding a pathological
// stylesheet that declares hundreds.
const maxCustomProperties = 30

// maxRankedValuesPerProperty bounds how many of each ranked CSS property's
// (color, font-family, etc. — see rankedCSSProperties) most-frequent
// distinct values BuildDigest keeps. Frequency is the actual signal (a
// color used 40 times across a stylesheet is the brand color; one used
// once is an edge case), so this caps the LONG TAIL, not the useful part.
const maxRankedValuesPerProperty = 8

// maxFontFaceFamilies and maxFontCDNHrefs bound the typefaces section — a
// real page rarely declares or links more than a handful of distinct font
// families; these are headroom above that, not a tight budget.
const (
	maxFontFaceFamilies = 10
	maxFontCDNHrefs     = 5
)

// maxCSSValueChars caps a single declared CSS value (ranked or custom-
// property) — every other per-item field in this file is already length-
// capped one way or another; without this, one pathological declared value
// (a box-shadow with many comma-separated layers) could dominate the whole
// budget on its own before DigestHardCapBytes' final truncation ever gets a
// say in which section loses ground. See maxHeadingChars in
// digest_structure.go for the same reasoning applied to heading text.
const maxCSSValueChars = 200

// rankedCSSProperties are the declared-value properties BuildDigest ranks
// by occurrence count (see maxRankedValuesPerProperty) — chosen because
// each one is a concrete, reusable design decision (a palette color, a
// typeface, a corner radius) a model can act on directly, unlike most of
// the rest of a stylesheet (layout, positioning, vendor-specific rules).
var rankedCSSProperties = []string{
	"color", "background-color", "border-color", "font-family",
	"font-size", "font-weight", "border-radius", "box-shadow", "letter-spacing",
}

// cssPropertyPatterns is built once, at package init — compiling
// len(rankedCSSProperties) regexps is a one-time cost, not something to
// repeat per fetched page. Each pattern requires its property name to be
// preceded only by "{"/";" and optional whitespace, not by \b alone: CSS
// uses "-" as a word-joining character regexes treat as a boundary, so a
// bare \bcolor\b would also match the "color" inside "background-color".
var cssPropertyPatterns = func() map[string]*regexp.Regexp {
	m := make(map[string]*regexp.Regexp, len(rankedCSSProperties))
	for _, p := range rankedCSSProperties {
		m[p] = regexp.MustCompile(`(?i)(?:^|[{;])\s*` + regexp.QuoteMeta(p) + `\s*:\s*([^;{}]+)`)
	}
	return m
}()

// customPropertyPattern matches a CSS custom property declaration anywhere
// in the stylesheet text — deliberately not scoped to :root, since a
// design system's tokens are sometimes declared per-component instead.
var customPropertyPattern = regexp.MustCompile(`(--[a-zA-Z0-9-]+)\s*:\s*([^;{}]+)`)

// fontFaceBlockPattern and fontFamilyValuePattern together extract a
// declared family name from each @font-face rule — two patterns rather
// than one, since font-family can appear anywhere inside the block, in any
// order relative to the other descriptors (src, font-weight, etc.).
var (
	fontFaceBlockPattern   = regexp.MustCompile(`(?is)@font-face\s*\{([^}]*)\}`)
	fontFamilyValuePattern = regexp.MustCompile(`(?i)font-family\s*:\s*([^;]+)`)
)

// fontCDNHosts are well-known font-hosting domains BuildDigest recognizes
// when scanning the document's own <link href>s — matched by exact host or
// subdomain (see isFontCDNHost), so "fonts.googleapis.com" also matches
// "www.fonts.googleapis.com" but not "notfonts.googleapis.com.evil.example".
var fontCDNHosts = []string{
	"fonts.googleapis.com", "fonts.gstatic.com", "use.typekit.net",
	"use.fontawesome.com", "fonts.adobe.com", "fast.fonts.net", "p.typekit.net",
}

type cssProp struct {
	name  string
	value string
}

type rankedValue struct {
	value string
	count int
}

type designTokens struct {
	customProps  []cssProp
	ranked       map[string][]rankedValue
	fontFaces    []string
	fontCDNHrefs []string
}

func extractDesignTokens(css, htmlSrc string) designTokens {
	ranked := make(map[string][]rankedValue, len(rankedCSSProperties))
	for _, prop := range rankedCSSProperties {
		if values := rankTopValues(css, cssPropertyPatterns[prop], maxRankedValuesPerProperty); len(values) > 0 {
			ranked[prop] = values
		}
	}
	return designTokens{
		customProps:  extractCustomProperties(css),
		ranked:       ranked,
		fontFaces:    extractFontFaceFamilies(css),
		fontCDNHrefs: extractFontCDNHrefs(htmlSrc),
	}
}

// normalizeCSSValue trims and collapses a raw regex-captured declaration
// value ("  #FFF  " -> "#FFF", "red !important" -> "red") so trivially
// different-looking captures of the same real value tally as one entry
// instead of splitting the count between them, then caps it at
// maxCSSValueChars (see that const's own doc comment).
func normalizeCSSValue(raw string) string {
	v := collapseWhitespace(raw)
	v = strings.TrimSpace(strings.TrimSuffix(v, "!important"))
	return truncateBytes(v, maxCSSValueChars)
}

// rankTopValues tallies every value pattern captures in css and returns
// the topN most frequent, most-frequent first, ties broken by first-seen
// order (via sort.SliceStable over a first-seen-ordered slice) so the
// result is deterministic rather than dependent on Go's map iteration
// order.
func rankTopValues(css string, pattern *regexp.Regexp, topN int) []rankedValue {
	counts := make(map[string]int)
	var order []string
	for _, m := range pattern.FindAllStringSubmatch(css, -1) {
		v := normalizeCSSValue(m[1])
		if v == "" {
			continue
		}
		if _, seen := counts[v]; !seen {
			order = append(order, v)
		}
		counts[v]++
	}
	sort.SliceStable(order, func(i, j int) bool {
		return counts[order[i]] > counts[order[j]]
	})
	if len(order) > topN {
		order = order[:topN]
	}
	out := make([]rankedValue, len(order))
	for i, v := range order {
		out[i] = rankedValue{value: v, count: counts[v]}
	}
	return out
}

func extractCustomProperties(css string) []cssProp {
	seen := make(map[string]bool)
	var out []cssProp
	for _, m := range customPropertyPattern.FindAllStringSubmatch(css, -1) {
		name := m[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, cssProp{name: name, value: normalizeCSSValue(m[2])})
		if len(out) >= maxCustomProperties {
			break
		}
	}
	return out
}

func extractFontFaceFamilies(css string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, block := range fontFaceBlockPattern.FindAllStringSubmatch(css, -1) {
		m := fontFamilyValuePattern.FindStringSubmatch(block[1])
		if m == nil {
			continue
		}
		name := strings.Trim(collapseWhitespace(m[1]), `"'`)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
		if len(out) >= maxFontFaceFamilies {
			break
		}
	}
	return out
}

func isFontCDNHost(host string) bool {
	host = strings.ToLower(host)
	for _, known := range fontCDNHosts {
		if host == known || strings.HasSuffix(host, "."+known) {
			return true
		}
	}
	return false
}

func extractFontCDNHrefs(htmlSrc string) []string {
	var out []string
	seen := make(map[string]bool)
	z := html.NewTokenizer(strings.NewReader(htmlSrc))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return out
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			if t.Data != "link" {
				continue
			}
			href := attrVal(t, "href")
			if href == "" || seen[href] {
				continue
			}
			u, err := url.Parse(href)
			if err != nil || !isFontCDNHost(u.Hostname()) {
				continue
			}
			seen[href] = true
			out = append(out, href)
			if len(out) >= maxFontCDNHrefs {
				return out
			}
		}
	}
}

func writeDesignTokensSection(b *strings.Builder, tokens designTokens) {
	hasRanked := false
	for _, values := range tokens.ranked {
		if len(values) > 0 {
			hasRanked = true
			break
		}
	}
	if len(tokens.customProps) == 0 && !hasRanked && len(tokens.fontFaces) == 0 && len(tokens.fontCDNHrefs) == 0 {
		return
	}
	b.WriteString("DESIGN TOKENS\n")
	if len(tokens.customProps) > 0 {
		b.WriteString("custom properties:\n")
		for _, p := range tokens.customProps {
			fmt.Fprintf(b, "  %s: %s\n", p.name, p.value)
		}
	}
	// Iterate rankedCSSProperties (a fixed slice), not tokens.ranked (a
	// map) directly, so section order is deterministic — Go map iteration
	// order is randomized, and this output feeds a model prompt, where
	// reordering runs between otherwise-identical fetches is exactly the
	// kind of pointless noise worth avoiding.
	for _, prop := range rankedCSSProperties {
		values := tokens.ranked[prop]
		if len(values) == 0 {
			continue
		}
		fmt.Fprintf(b, "%s (most used first):\n", prop)
		for _, v := range values {
			fmt.Fprintf(b, "  %s (x%d)\n", v.value, v.count)
		}
	}
	if len(tokens.fontFaces) > 0 {
		fmt.Fprintf(b, "@font-face families: %s\n", strings.Join(tokens.fontFaces, ", "))
	}
	if len(tokens.fontCDNHrefs) > 0 {
		fmt.Fprintf(b, "font CDN links: %s\n", strings.Join(tokens.fontCDNHrefs, ", "))
	}
	b.WriteString("\n")
}

package urlfetch

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

// maxCustomProperties bounds how many CSS custom properties BuildDigest
// lists verbatim — generous headroom above a real :root block's typical size.
const maxCustomProperties = 30

// maxRankedValuesPerProperty bounds how many of each ranked property's
// most-frequent values are kept — frequency is the real signal (a color
// used 40 times is the brand color), so this caps the long tail only.
const maxRankedValuesPerProperty = 8

// maxFontFaceFamilies and maxFontCDNHrefs bound the typefaces section —
// headroom above what a real page typically declares/links.
const (
	maxFontFaceFamilies = 10
	maxFontCDNHrefs     = 5
)

// maxCSSValueChars caps one declared CSS value so a pathological value
// (a box-shadow with many layers) can't dominate the budget before
// DigestHardCapBytes' truncation runs.
const maxCSSValueChars = 200

// rankedCSSProperties are the properties ranked by occurrence count — each
// is a concrete, reusable design decision a model can act on directly.
var rankedCSSProperties = []string{
	"color", "background-color", "border-color", "font-family",
	"font-size", "font-weight", "border-radius", "box-shadow", "letter-spacing",
}

// cssPropertyPatterns is built once at package init. Each pattern requires
// its property preceded only by "{"/";" + whitespace, not bare \b: CSS's "-"
// is a regex word-boundary character, so bare \bcolor\b would also match
// inside "background-color".
var cssPropertyPatterns = func() map[string]*regexp.Regexp {
	m := make(map[string]*regexp.Regexp, len(rankedCSSProperties))
	for _, p := range rankedCSSProperties {
		m[p] = regexp.MustCompile(`(?i)(?:^|[{;])\s*` + regexp.QuoteMeta(p) + `\s*:\s*([^;{}]+)`)
	}
	return m
}()

// customPropertyPattern matches anywhere in the stylesheet, not just :root —
// design-system tokens are sometimes declared per-component instead.
var customPropertyPattern = regexp.MustCompile(`(--[a-zA-Z0-9-]+)\s*:\s*([^;{}]+)`)

// fontFaceBlockPattern and fontFamilyValuePattern are two patterns, not one,
// since font-family can appear anywhere inside an @font-face block.
var (
	fontFaceBlockPattern   = regexp.MustCompile(`(?is)@font-face\s*\{([^}]*)\}`)
	fontFamilyValuePattern = regexp.MustCompile(`(?i)font-family\s*:\s*([^;]+)`)
)

// fontCDNHosts are matched by exact host or subdomain (see isFontCDNHost),
// so "fonts.googleapis.com" also matches "www.fonts.googleapis.com" but not
// a lookalike like "notfonts.googleapis.com.evil.example".
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

// normalizeCSSValue trims/collapses a captured value ("  #FFF  " -> "#FFF",
// "red !important" -> "red") so equivalent captures tally as one entry.
func normalizeCSSValue(raw string) string {
	v := collapseWhitespace(raw)
	v = strings.TrimSpace(strings.TrimSuffix(v, "!important"))
	return truncateBytes(v, maxCSSValueChars)
}

// rankTopValues returns the topN most frequent values pattern captures in
// css, ties broken by first-seen order for deterministic output.
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
	// Iterate the fixed slice, not the map, so section order is deterministic
	// (this feeds a model prompt — reordering between fetches is just noise).
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

package urlfetch

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

// digestSoftBudgetBytes is the target size BuildDigest aims for through its
// per-section caps (see each section's own const below) — not separately
// enforced here as a hard cutoff, since negotiating it precisely between
// sections would need real coordination for no real benefit: the per-
// section caps are sized so a typical real page lands close to this on its
// own, and DigestHardCapBytes is the actual backstop for anything that
// doesn't.
const digestSoftBudgetBytes = 10 * 1024

// DigestHardCapBytes is the absolute ceiling BuildDigest enforces on its
// output, unconditionally, as a final truncation pass — see BuildDigest's
// own tail. A model working from a reference is still working primarily
// from the merchant's own theme files and prompt; this keeps one reference
// from ever growing into a second dominant input the way raw markup could.
// Exported so a caller that persists a digest (themebuild.Service, for
// carry-forward) can recognize a stored one that hit this cap without a
// second, drifting copy of the number — see
// themebuild.looksTruncatedByStoredLength.
const DigestHardCapBytes = 16 * 1024

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

// maxHeadings and maxLandmarks bound the structure outline; landmarkPreviewChars
// bounds each landmark's one-line text preview.
const (
	maxHeadings          = 40
	maxLandmarks         = 20
	landmarkPreviewChars = 120
)

// maxCopyChars bounds the visible-body-text section; maxInteractiveLabels
// and interactiveLabelMaxChars bound the separate interactive-labels list
// (link/button/form-field text) — see BuildDigest's own doc comment for
// why these are kept apart from the general copy.
const (
	maxCopyChars             = 2000
	maxInteractiveLabels     = 40
	interactiveLabelMaxChars = 60
	metaDescriptionMaxChars  = 300
	imageAltPreviewMaxChars  = 100
	maxImages                = 20
)

// maxHeadingChars and maxCSSValueChars cap a single heading's text and a
// single CSS value (ranked or custom-property) respectively — every other
// per-item field in this file is already length-capped one way or another;
// without these two, one pathological heading or declared value (a
// merchant's own copy dumped into an <h1>, a box-shadow with many
// comma-separated layers) could dominate the whole budget on its own
// before DigestHardCapBytes' final truncation ever gets a say in which
// section loses ground.
const (
	maxHeadingChars  = 200
	maxCSSValueChars = 200
)

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
	whitespaceRunPattern   = regexp.MustCompile(`\s+`)
)

// fontCDNHosts are well-known font-hosting domains BuildDigest recognizes
// when scanning the document's own <link href>s — matched by exact host or
// subdomain (see isFontCDNHost), so "fonts.googleapis.com" also matches
// "www.fonts.googleapis.com" but not "notfonts.googleapis.com.evil.example".
var fontCDNHosts = []string{
	"fonts.googleapis.com", "fonts.gstatic.com", "use.typekit.net",
	"use.fontawesome.com", "fonts.adobe.com", "fast.fonts.net", "p.typekit.net",
}

// excludedTextTags never contribute to any text-derived section (copy,
// headings, landmark previews, interactive labels) — their content isn't
// visible page text, and script/noscript content in particular is exactly
// the kind of thing the untrusted-content framing around a digest (see
// this file's own doc comment on BuildDigest) exists to keep the model
// from treating as instructions. "title" is here too even though it isn't
// executable: it's <head> metadata (a browser tab label), not body copy —
// it's already surfaced on its own via extractPage/pageIdentity.title, and
// without excluding it here it would otherwise leak into COPY (and,
// worse, count toward "effectively no copy" NOT being true for a page
// whose only real text is its <title> — exactly the client-rendered-shell
// case Digest.Empty exists to catch).
var excludedTextTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true, "title": true,
}

// headingTags maps a heading tag name to its outline depth/level.
var headingTags = map[string]int{"h1": 1, "h2": 2, "h3": 3}

// landmarkTags are the elements STRUCTURE's outline previews — the HTML
// landmark roles a merchant's own section rhythm is usually built from.
var landmarkTags = map[string]bool{
	"header": true, "nav": true, "main": true, "section": true,
	"article": true, "aside": true, "footer": true,
}

// interactiveLabelTags are the elements whose visible text BuildDigest
// collects separately from general copy (see extractCopy) — what a
// merchant usually means by "match the buttons/nav" on a reference page.
var interactiveLabelTags = map[string]bool{"a": true, "button": true, "label": true}

// Digest is BuildDigest's result.
type Digest struct {
	// Text is the finished, labelled plain-text digest, capped at
	// DigestHardCapBytes — what themebuild sends to the model for a
	// fetched reference URL in place of raw HTML (see
	// Service.fetchReferenceURL).
	Text string
	// Title is the page's <title> text, surfaced separately from Text for
	// a caller's own narration (see doGenerate's fetching-link-finished
	// event, which reports the page title alongside the stylesheet count).
	Title string
	// Empty reports whether extraction found no headings, no landmark
	// elements, and no meaningful visible body copy — the client-rendered-
	// shell signature (a React/Vue app whose server-sent HTML is just an
	// empty mount point). Measured on the digest's own EXTRACTED text, not
	// on raw HTML byte length, since a shell's raw markup can easily be
	// several KB of framework boilerplate despite having nothing a model
	// can actually use. Callers should treat this the same as an empty
	// sanitized-HTML result always has (see themebuild's
	// ReferenceURLEmptyAfterSanitize).
	Empty bool
	// Truncated reports whether BuildDigest's own DigestHardCapBytes
	// truncation actually cut the assembled text — independent of, and in
	// addition to, whatever truncation happened upstream fetching the raw
	// HTML (Result.Truncated): a page can fetch in full and still produce
	// more extracted headings/copy/design-tokens than DigestHardCapBytes
	// allows once combined. A caller (themebuild.Service.fetchReferenceURL)
	// ORs this together with Result.Truncated so the merchant-facing "this
	// copy was cut short" note fires for either cause, not just the first.
	Truncated bool
}

// BuildDigest is a pure transformation — (html, css) in, a compact
// structured text digest out — no network, no fetching of its own. It
// exists because raw fetched markup is the wrong payload for a model
// writing Liquid: up to 300KB of DOM is mostly framework wrapper divs,
// hashed class names, and tracking attributes, none of it design signal,
// and every byte of it both slows generation and crowds the merchant's own
// theme files out of the model's attention. The digest instead surfaces
// what actually closes the "uploaded file looks better than a pasted link"
// gap — design tokens the model can name and reuse, a structural outline
// it can mirror, and the page's real copy — ranked and capped so what
// survives truncation is the most design-relevant material first (see each
// section's own ordering below).
//
// The digest is DERIVED content, but it is exactly as untrusted as the raw
// HTML it's built from: extraction here does not sanitize against a page
// author trying to inject model-directed instructions into a heading,
// alt text, or body copy. The injection-guard framing in
// themebuild.promptWithHTMLAttachment still applies to a digest exactly as
// it did to raw markup — callers must not treat this as safe to skip that
// framing just because it's shorter and structured.
//
// finalURL is the page's resolved location (Result.FinalURL — may be nil
// in a test that has no real fetch behind it, in which case the identity
// section simply omits the URL line); htmlSrc is the fetched HTML; css is
// the combined stylesheet text (see Fetcher.FetchStylesheets) — empty is
// fine and just produces a digest with no design-tokens section, not an
// error.
func BuildDigest(finalURL *url.URL, htmlSrc, css string) Digest {
	page := extractPage(htmlSrc)
	tokens := extractDesignTokens(css, htmlSrc)
	structure := extractStructure(htmlSrc)
	body := extractCopy(htmlSrc)
	images := extractImages(htmlSrc)

	var b strings.Builder
	writeIdentitySection(&b, finalURL, page)
	writeDesignTokensSection(&b, tokens)
	writeStructureSection(&b, structure)
	writeCopySection(&b, body)
	// IMAGES is the lowest-priority section (see this function's own doc
	// comment on section ordering) — it's the one dropped outright when
	// the soft budget is already spent before reaching it, rather than
	// letting every section grow and relying solely on the hard-cap
	// truncation at the tail below, which would cut whatever section
	// happens to land across the boundary rather than the least useful one.
	if b.Len() < digestSoftBudgetBytes {
		writeImagesSection(&b, images)
	}

	text := strings.TrimSpace(b.String())
	truncated := len(text) > DigestHardCapBytes
	if truncated {
		text = trimIncompleteTrailingRune(text[:DigestHardCapBytes])
	}

	empty := len(structure.headings) == 0 && len(structure.landmarks) == 0 && strings.TrimSpace(body.bodyText) == ""

	return Digest{Text: text, Title: page.title, Empty: empty, Truncated: truncated}
}

// truncateBytes cuts s to at most n bytes, backing up to a valid UTF-8
// boundary (see trimIncompleteTrailingRune in fetch.go) — used throughout
// this file for the many small per-field caps (a preview, a label, a
// description), each of which is a plain byte-length cut with no tag-
// boundary concept the way HTML truncation needs.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return trimIncompleteTrailingRune(s[:n])
}

// collapseWhitespace replaces every run of whitespace (spaces, tabs,
// newlines — exactly what real markup's indentation and line-wrapping
// leaves in a text node) with a single space and trims the ends, turning
// "  Home\n    Page  " into "Home Page".
func collapseWhitespace(s string) string {
	return strings.TrimSpace(whitespaceRunPattern.ReplaceAllString(s, " "))
}

// --- page identity ---------------------------------------------------

type pageIdentity struct {
	title       string
	description string
	lang        string
}

func extractPage(htmlSrc string) pageIdentity {
	var page pageIdentity
	inTitle := false
	var titleBuf strings.Builder
	z := html.NewTokenizer(strings.NewReader(htmlSrc))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return page
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			switch t.Data {
			case "html":
				if lang := attrVal(t, "lang"); lang != "" && page.lang == "" {
					page.lang = lang
				}
			case "title":
				// First <title> wins — a second one is malformed markup,
				// not a real second choice.
				if page.title == "" {
					inTitle = true
					titleBuf.Reset()
				}
			case "meta":
				if page.description == "" && strings.EqualFold(attrVal(t, "name"), "description") {
					page.description = truncateBytes(collapseWhitespace(attrVal(t, "content")), metaDescriptionMaxChars)
				}
			}
		case html.EndTagToken:
			if inTitle {
				t := z.Token()
				if t.Data == "title" {
					page.title = collapseWhitespace(titleBuf.String())
					inTitle = false
				}
			}
		case html.TextToken:
			if inTitle {
				titleBuf.WriteString(z.Token().Data)
			}
		}
	}
}

func writeIdentitySection(b *strings.Builder, finalURL *url.URL, page pageIdentity) {
	b.WriteString("PAGE\n")
	if finalURL != nil {
		fmt.Fprintf(b, "url: %s\n", finalURL.String())
	}
	if page.title != "" {
		fmt.Fprintf(b, "title: %s\n", page.title)
	}
	if page.description != "" {
		fmt.Fprintf(b, "description: %s\n", page.description)
	}
	if page.lang != "" {
		fmt.Fprintf(b, "lang: %s\n", page.lang)
	}
	b.WriteString("\n")
}

// --- design tokens -----------------------------------------------------

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

// --- structure outline ---------------------------------------------------

type heading struct {
	level int
	text  string
}

type landmark struct {
	tag     string
	preview string
}

type structureInfo struct {
	headings  []heading
	landmarks []landmark
}

// landmarkFrame tracks one currently-open landmark element's accumulating
// preview text — a stack of these, not a single variable, since landmarks
// nest constantly in real markup (a <nav> inside a <header>, a <section>
// inside <main>).
type landmarkFrame struct {
	tag  string
	buf  strings.Builder
	done bool // stop appending once buf has enough for a preview, but the frame stays open so close-tag bookkeeping (matching by tag name) stays correct
}

func extractStructure(htmlSrc string) structureInfo {
	var out structureInfo
	headingLevel := 0
	var headingBuf strings.Builder
	var landmarkStack []*landmarkFrame
	excludeDepth := 0

	z := html.NewTokenizer(strings.NewReader(htmlSrc))
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return out
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			if excludedTextTags[t.Data] {
				if tt == html.StartTagToken {
					excludeDepth++
				}
				continue
			}
			if level, ok := headingTags[t.Data]; ok && tt == html.StartTagToken {
				headingLevel = level
				headingBuf.Reset()
				continue
			}
			if landmarkTags[t.Data] && tt == html.StartTagToken {
				landmarkStack = append(landmarkStack, &landmarkFrame{tag: t.Data})
			}
		case html.EndTagToken:
			t := z.Token()
			if excludedTextTags[t.Data] {
				if excludeDepth > 0 {
					excludeDepth--
				}
				continue
			}
			if level, ok := headingTags[t.Data]; ok && headingLevel == level {
				text := truncateBytes(collapseWhitespace(headingBuf.String()), maxHeadingChars)
				if text != "" && len(out.headings) < maxHeadings {
					out.headings = append(out.headings, heading{level: level, text: text})
				}
				headingLevel = 0
				continue
			}
			if landmarkTags[t.Data] {
				for i := len(landmarkStack) - 1; i >= 0; i-- {
					if landmarkStack[i].tag != t.Data {
						continue
					}
					frame := landmarkStack[i]
					landmarkStack = append(landmarkStack[:i], landmarkStack[i+1:]...)
					preview := collapseWhitespace(frame.buf.String())
					if preview != "" && len(out.landmarks) < maxLandmarks {
						out.landmarks = append(out.landmarks, landmark{tag: frame.tag, preview: truncateBytes(preview, landmarkPreviewChars)})
					}
					break
				}
			}
		case html.TextToken:
			if excludeDepth > 0 {
				continue
			}
			text := z.Token().Data
			if headingLevel > 0 {
				headingBuf.WriteString(text)
			}
			for _, frame := range landmarkStack {
				if !frame.done {
					frame.buf.WriteString(text)
					if frame.buf.Len() >= landmarkPreviewChars {
						frame.done = true
					}
				}
			}
		}
	}
}

func writeStructureSection(b *strings.Builder, s structureInfo) {
	if len(s.headings) == 0 && len(s.landmarks) == 0 {
		return
	}
	b.WriteString("STRUCTURE\n")
	if len(s.headings) > 0 {
		b.WriteString("headings:\n")
		for _, h := range s.headings {
			fmt.Fprintf(b, "  %sh%d: %s\n", strings.Repeat("  ", h.level-1), h.level, h.text)
		}
	}
	if len(s.landmarks) > 0 {
		b.WriteString("landmarks:\n")
		for _, l := range s.landmarks {
			fmt.Fprintf(b, "  <%s>: %s\n", l.tag, l.preview)
		}
	}
	b.WriteString("\n")
}

// --- copy and interactive labels ---------------------------------------

type copyInfo struct {
	bodyText string
	labels   []string
}

// labelFrame mirrors landmarkFrame for interactive elements (a, button,
// label) — also stack-based since a <label> commonly wraps other markup
// (an <input> plus its own text) rather than being a leaf.
type labelFrame struct {
	tag string
	buf strings.Builder
}

func extractCopy(htmlSrc string) copyInfo {
	var body strings.Builder
	var labels []string
	seenLabels := make(map[string]bool)
	var labelStack []*labelFrame
	excludeDepth := 0

	addLabel := func(text string) {
		text = truncateBytes(text, interactiveLabelMaxChars)
		if text == "" || seenLabels[text] || len(labels) >= maxInteractiveLabels {
			return
		}
		seenLabels[text] = true
		labels = append(labels, text)
	}

	z := html.NewTokenizer(strings.NewReader(htmlSrc))
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return copyInfo{bodyText: truncateBytes(collapseWhitespace(body.String()), maxCopyChars), labels: labels}
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			if excludedTextTags[t.Data] {
				if tt == html.StartTagToken {
					excludeDepth++
				}
				continue
			}
			if interactiveLabelTags[t.Data] && tt == html.StartTagToken {
				labelStack = append(labelStack, &labelFrame{tag: t.Data})
				continue
			}
			// input[type=submit|button|reset] has no closing content of its
			// own to capture text from — its visible label is its value
			// attribute instead.
			if t.Data == "input" {
				switch strings.ToLower(attrVal(t, "type")) {
				case "submit", "button", "reset":
					addLabel(collapseWhitespace(attrVal(t, "value")))
				}
			}
		case html.EndTagToken:
			t := z.Token()
			if excludedTextTags[t.Data] {
				if excludeDepth > 0 {
					excludeDepth--
				}
				continue
			}
			if interactiveLabelTags[t.Data] {
				for i := len(labelStack) - 1; i >= 0; i-- {
					if labelStack[i].tag != t.Data {
						continue
					}
					frame := labelStack[i]
					labelStack = append(labelStack[:i], labelStack[i+1:]...)
					addLabel(collapseWhitespace(frame.buf.String()))
					break
				}
			}
		case html.TextToken:
			if excludeDepth > 0 {
				continue
			}
			text := z.Token().Data
			body.WriteString(text)
			body.WriteByte(' ')
			for _, frame := range labelStack {
				frame.buf.WriteString(text)
			}
		}
	}
}

func writeCopySection(b *strings.Builder, c copyInfo) {
	if c.bodyText == "" && len(c.labels) == 0 {
		return
	}
	b.WriteString("COPY\n")
	if c.bodyText != "" {
		fmt.Fprintf(b, "%s\n", c.bodyText)
	}
	if len(c.labels) > 0 {
		fmt.Fprintf(b, "interactive labels: %s\n", strings.Join(c.labels, ", "))
	}
	b.WriteString("\n")
}

// --- images ---------------------------------------------------------------

type imageInfo struct {
	alt  string
	stem string
}

// filenameStem reduces an <img> src to a short filename-derived hint — the
// full URL is mostly noise (a CDN host, a cache-busting query string, a
// content hash), but the base filename itself ("hero-banner") is often a
// real, human-chosen clue about the image's subject.
func filenameStem(src string) string {
	if src == "" {
		return ""
	}
	if i := strings.IndexAny(src, "?#"); i >= 0 {
		src = src[:i]
	}
	if i := strings.LastIndexByte(src, '/'); i >= 0 {
		src = src[i+1:]
	}
	if i := strings.LastIndexByte(src, '.'); i > 0 {
		src = src[:i]
	}
	return src
}

func extractImages(htmlSrc string) []imageInfo {
	var out []imageInfo
	seen := make(map[string]bool)
	z := html.NewTokenizer(strings.NewReader(htmlSrc))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return out
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			if t.Data != "img" {
				continue
			}
			alt := truncateBytes(collapseWhitespace(attrVal(t, "alt")), imageAltPreviewMaxChars)
			stem := filenameStem(attrVal(t, "src"))
			if alt == "" && stem == "" {
				continue
			}
			key := alt + "|" + stem
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, imageInfo{alt: alt, stem: stem})
			if len(out) >= maxImages {
				return out
			}
		}
	}
}

func writeImagesSection(b *strings.Builder, images []imageInfo) {
	if len(images) == 0 {
		return
	}
	b.WriteString("IMAGES\n")
	for _, img := range images {
		switch {
		case img.stem != "" && img.alt != "":
			fmt.Fprintf(b, "  %s (alt: %s)\n", img.stem, img.alt)
		case img.alt != "":
			fmt.Fprintf(b, "  (alt: %s)\n", img.alt)
		default:
			fmt.Fprintf(b, "  %s\n", img.stem)
		}
	}
}

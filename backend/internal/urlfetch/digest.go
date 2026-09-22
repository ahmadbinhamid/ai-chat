package urlfetch

import (
	"net/url"
	"regexp"
	"strings"
)

// digestSoftBudgetBytes is BuildDigest's soft target via per-section caps;
// DigestHardCapBytes is the real hard backstop.
const digestSoftBudgetBytes = 10 * 1024

// DigestHardCapBytes is the absolute ceiling BuildDigest enforces. Exported
// so themebuild.Service can recognize a stored digest that hit this cap.
const DigestHardCapBytes = 16 * 1024

// whitespaceRunPattern backs collapseWhitespace — package-level so it's
// compiled once.
var whitespaceRunPattern = regexp.MustCompile(`\s+`)

// excludedTextTags never contribute to a text-derived section. "title" is
// excluded too since it's surfaced separately via pageIdentity, and leaving
var excludedTextTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true, "title": true,
}

// Digest is BuildDigest's result.
type Digest struct {
	// Text is the finished, labelled plain-text digest, capped at DigestHardCapBytes.
	Text string
	// Title is the page's <title>, surfaced separately for a caller's own narration.
	Title string
	// Empty is the client-rendered-shell signature: no headings, landmarks, or body copy.
	Empty bool
	// Truncated reports whether the hard-cap cut the text, independent of any
	// upstream HTML truncation (Result.Truncated).
	Truncated bool
}

// BuildDigest extracts design tokens, structure, and copy from raw HTML/CSS, ranked for truncation robustness. Not sanitized; caller must inject-guard as with raw markup. finalURL/css may be nil/empty.
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
	// IMAGES is lowest-priority: dropped outright once the soft budget is
	// spent, rather than letting the hard-cap truncation below cut whatever
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

// truncateBytes cuts s to at most n bytes at a valid UTF-8 boundary — used
// throughout digest_*.go for small per-field caps (preview, label, description).
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return trimIncompleteTrailingRune(s[:n])
}

// collapseWhitespace turns "  Home\n    Page  " into "Home Page".
func collapseWhitespace(s string) string {
	return strings.TrimSpace(whitespaceRunPattern.ReplaceAllString(s, " "))
}

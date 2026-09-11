package urlfetch

import (
	"net/url"
	"regexp"
	"strings"
)

// digestSoftBudgetBytes is the target size BuildDigest aims for through its
// per-section caps (see each section's own const, in the digest_*.go files
// this one orchestrates) — not separately enforced here as a hard cutoff,
// since negotiating it precisely between sections would need real
// coordination for no real benefit: the per-section caps are sized so a
// typical real page lands close to this on its own, and DigestHardCapBytes
// is the actual backstop for anything that doesn't.
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

// whitespaceRunPattern backs collapseWhitespace below — package-level so
// it's compiled once, not per call; every digest_*.go extraction function
// funnels its own collected text through collapseWhitespace, so this runs
// far too often to rebuild per call.
var whitespaceRunPattern = regexp.MustCompile(`\s+`)

// excludedTextTags never contribute to any text-derived section (copy,
// headings, landmark previews, interactive labels — see digest_structure.go
// and digest_copy.go, both of which read this) — their content isn't
// visible page text, and script/noscript content in particular is exactly
// the kind of thing the untrusted-content framing around a digest (see
// this file's own doc comment on BuildDigest) exists to keep the model
// from treating as instructions. "title" is here too even though it isn't
// executable: it's <head> metadata (a browser tab label), not body copy —
// it's already surfaced on its own via extractPage/pageIdentity.title
// (digest_identity.go), and without excluding it here it would otherwise
// leak into COPY (and, worse, count toward "effectively no copy" NOT being
// true for a page whose only real text is its <title> — exactly the
// client-rendered-shell case Digest.Empty exists to catch).
var excludedTextTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true, "title": true,
}

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
//
// The five sections (identity, design tokens, structure, copy, images)
// each live in their own digest_*.go file — this function is the only
// place that reaches across all of them, in the priority order that
// determines what survives truncation first.
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
// the digest_*.go files for the many small per-field caps (a preview, a
// label, a description), each of which is a plain byte-length cut with no
// tag-boundary concept the way HTML truncation needs.
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

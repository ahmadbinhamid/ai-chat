package themebuild

import (
	"regexp"
	"strings"
)

// executableScriptTypes are the <script type="..."> values a browser will
// actually execute — an absent/empty type, "module", and the legacy
// JavaScript MIME types the HTML spec recognizes. Any other type value is
// inert to the browser: it's never executed, only ever read as data by
// whatever code goes looking for it (a JSON island, a template, this
// package's own attachment-content string). Stripping a <script> block
// unconditionally used to also delete an attached file's actual content
// whenever that content happened to live inside a non-executable script
// tag — see e.g. a "bundled" single-file HTML export, which stores its
// real page markup in <script type="__bundler/template"> and its data in
// <script type="text/x-dc">: both are inert containers the export's own
// unpacking script reads as text, never runs, but a blanket script-strip
// deleted them anyway, leaving nothing but the export's loading-screen
// markup for the model to read. Keyed by the type's value lowercased with
// any ";charset=..." (or similar) parameter dropped, matching how a
// browser itself parses the attribute.
var executableScriptTypes = map[string]bool{
	"":                         true,
	"module":                   true,
	"text/javascript":          true,
	"text/ecmascript":          true,
	"text/jscript":             true,
	"text/livescript":          true,
	"text/x-ecmascript":        true,
	"text/x-javascript":        true,
	"application/ecmascript":   true,
	"application/javascript":   true,
	"application/x-ecmascript": true,
	"application/x-javascript": true,
}

// scriptBlockRe matches one whole <script ...>...</script> block —
// SanitizeHTMLAttachment decides per-match, via isExecutableScriptTag,
// whether to actually strip it.
var scriptBlockRe = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>`)

// scriptOpenTagRe extracts just a script block's opening tag, so
// isExecutableScriptTag has something to look a type attribute up in
// without re-scanning the (possibly large) block body.
var scriptOpenTagRe = regexp.MustCompile(`(?is)^<script\b[^>]*>`)

// scriptTypeAttrRe finds a type="..."/type='...'/type=bare attribute
// value inside a script tag's opening tag — whichever quoting style is
// present; HTML permits all three.
var scriptTypeAttrRe = regexp.MustCompile(`(?is)\btype\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)

// isExecutableScriptTag reports whether openTag (a <script ...> opening
// tag, as matched by scriptOpenTagRe) is one a browser would actually
// execute — see executableScriptTypes' own doc comment for what that
// means and why it isn't "every script tag."
func isExecutableScriptTag(openTag string) bool {
	m := scriptTypeAttrRe.FindStringSubmatch(openTag)
	if m == nil {
		return true // no type attribute at all -> the default, executable type
	}
	typ := m[1] + m[2] + m[3] // exactly one of these three groups is non-empty
	if i := strings.IndexByte(typ, ';'); i >= 0 {
		typ = typ[:i]
	}
	return executableScriptTypes[strings.ToLower(strings.TrimSpace(typ))]
}

// dataURIRe strips embedded base64 data: URIs — src="data:image/png;
// base64,...", CSS url(data:...), etc. This is what actually blows up a
// "save page as HTML, single file" export's size (every image inlined as
// base64 can turn a real page's few hundred KB of markup/CSS into several
// MB), and it's dead weight for the model either way: it can't see pixels
// inside a text-embedded data URI the way it can a real image content
// block (see the image-attachment feature) — only real image/* blocks are
// ever actually rendered as vision input. Replaced with a short, harmless
// placeholder (not deleted outright) so the surrounding markup — the
// attribute's quotes, a CSS url(...) call — stays syntactically valid.
var dataURIRe = regexp.MustCompile(`data:[a-zA-Z0-9.+/-]+;base64,[A-Za-z0-9+/=]+`)

// svgBlockRe matches one whole inline <svg>...</svg> element — icon/logo
// vector markup, the dominant source of bulk in real-world fetched pages
// (see the link-fetch feature): a modern site's icon sprite sheet or a
// handful of inline logo/icon SVGs routinely runs to hundreds of KB of path
// coordinate data, dwarfing the actual page markup/copy. That data is
// exactly as useless to the model as an embedded base64 image (see
// dataURIRe above) — it can't see rendered vector graphics in path-command
// text any more than it can see pixels in a data: URI — so it's stripped
// the same way: dropped rather than kept toward the byte budget. Matches a
// self-closing <svg .../> too, since an empty/icon-font-only svg element
// can take that form.
var svgBlockRe = regexp.MustCompile(`(?is)<svg\b[^>]*?(?:/>|>.*?</svg\s*>)`)

// longBase64RunRe matches a long run of base64-alphabet characters with no
// data: URI prefix to key off — the shape a binary asset (a font, an
// image, a whole minified JS bundle) takes when embedded as a plain JSON
// string value rather than a data: URI, exactly what a "bundled" single-
// file HTML export's own asset manifest does (kept in place now by
// isExecutableScriptTag, since that script type is inert data, not
// something to delete outright — see its own doc comment). A run this
// long is never something a model needs verbatim as page content or a
// design reference; keeping it only spends the byte budget
// (service.go's MaxHTMLAttachmentBytes) on data nothing downstream reads
// as text. 400 chars is comfortably above any real copy, config value, or
// token (a JWT's longest single segment, a hash, a UUID) would ever run
// contiguously in the base64 alphabet — real prose/JSON/markup breaks
// that alphabet with spaces, punctuation, or structural characters far
// sooner. Replaced with nothing (matching dataURIRe's "keep the
// surrounding syntax valid, drop only the payload" approach — the
// original was a plain quoted string, and an empty string is still one).
var longBase64RunRe = regexp.MustCompile(`[A-Za-z0-9+/]{400,}=*`)

// SanitizeHTMLAttachment removes executable scripts, inline SVG markup, and
// embedded base64 assets from an uploaded HTML file before it's stored or
// sent to the model — see Generate's own use of this, right before the
// post-strip MaxHTMLAttachmentBytes check. Only strips a <script> block whose type
// isExecutableScriptTag says a browser would actually run (see that
// function's own doc comment) — a non-executable one (a JSON/template
// data island) is left in place, since removing it destroys real content
// without any safety benefit: it was never going to execute either way.
// Its payload is still subject to the same long-base64-run stripping as
// the rest of the document, though — see longBase64RunRe — since a kept
// script block can just as easily be carrying inlined binary assets as
// real content.
func SanitizeHTMLAttachment(html string) string {
	html = scriptBlockRe.ReplaceAllStringFunc(html, func(block string) string {
		if isExecutableScriptTag(scriptOpenTagRe.FindString(block)) {
			return ""
		}
		return block
	})
	html = svgBlockRe.ReplaceAllString(html, "")
	html = dataURIRe.ReplaceAllString(html, "data:,")
	return longBase64RunRe.ReplaceAllString(html, "")
}

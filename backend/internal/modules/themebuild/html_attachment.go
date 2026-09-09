package themebuild

import "regexp"

// scriptTagRe strips <script>...</script> blocks from an attached HTML
// file before it's ever stored or sent to the model — script content is
// irrelevant to the design/structure reference the merchant actually wants
// and is the one part of an HTML file that could otherwise execute
// somewhere downstream if this content were ever rendered rather than just
// read as text. This does NOT address prompt injection via plain visible
// text (see promptWithHTMLAttachment's framing for that) — there's no
// technical strip for that without destroying the reference value.
var scriptTagRe = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>`)

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

// SanitizeHTMLAttachment removes scripts and embedded base64 assets from
// an uploaded HTML file before it's stored or sent to the model — see
// Generate's own use of this, right before the post-strip
// MaxHTMLAttachmentBytes check.
func SanitizeHTMLAttachment(html string) string {
	html = scriptTagRe.ReplaceAllString(html, "")
	return dataURIRe.ReplaceAllString(html, "data:,")
}

package themebuild

import (
	"regexp"
	"strings"
)

// executableScriptTypes are the <script type="..."> values a browser will actually execute;
// a non-executable type (e.g. a JSON/template island) must be kept, not stripped, or real content is lost.
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

// scriptBlockRe matches one whole <script ...>...</script> block.
var scriptBlockRe = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>`)

// scriptOpenTagRe extracts a script block's opening tag, to check its type without rescanning the body.
var scriptOpenTagRe = regexp.MustCompile(`(?is)^<script\b[^>]*>`)

// scriptTypeAttrRe finds a type attribute value in any of HTML's three quoting styles.
var scriptTypeAttrRe = regexp.MustCompile(`(?is)\btype\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)

// isExecutableScriptTag reports whether a browser would actually execute this <script> tag.
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

// dataURIRe strips embedded base64 data: URIs, which can blow up an exported page's size and
// are useless to the model (it can't see pixels in text). Replaced with a placeholder to keep surrounding markup valid.
var dataURIRe = regexp.MustCompile(`data:[a-zA-Z0-9.+/-]+;base64,[A-Za-z0-9+/=]+`)

// svgBlockRe matches one whole inline <svg>...</svg> element — icon/logo path data that's
// often hundreds of KB and, like dataURIRe, useless to the model as text.
var svgBlockRe = regexp.MustCompile(`(?is)<svg\b[^>]*?(?:/>|>.*?</svg\s*>)`)

// longBase64RunRe matches a base64-alphabet run with no data: URI prefix — a binary asset embedded
// as a plain JSON string. 400 chars is well above any real prose/JSON/markup token length.
var longBase64RunRe = regexp.MustCompile(`[A-Za-z0-9+/]{400,}=*`)

// SanitizeHTMLAttachment strips executable scripts, inline SVG, and embedded base64 assets from
// an uploaded HTML file. Non-executable script blocks are kept but still get base64-run stripping.
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

package themebuild

import (
	"strings"
	"testing"
)

func TestSanitizeHTMLAttachment(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantIn    []string // substrings that must survive
		wantNotIn []string // substrings that must NOT survive
	}{
		{
			name:      "strips a script tag",
			in:        `<p>hello</p><script>alert('hi')</script><p>world</p>`,
			wantIn:    []string{"<p>hello</p>", "<p>world</p>"},
			wantNotIn: []string{"<script", "alert"},
		},
		{
			name:      "strips a script tag with attributes",
			in:        `<script type="text/javascript" src="evil.js">fetch('http://evil.com')</script><h1>Title</h1>`,
			wantIn:    []string{"<h1>Title</h1>"},
			wantNotIn: []string{"<script", "evil.js", "fetch"},
		},
		{
			name:      "strips an inline base64 image data URI",
			in:        `<img src="data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=">`,
			wantIn:    []string{`<img src="data:,">`},
			wantNotIn: []string{"iVBORw0KGgo"},
		},
		{
			name:      "strips a base64 data URI inside a CSS url()",
			in:        `<style>.x{background:url(data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=)}</style>`,
			wantIn:    []string{"url(data:,)"},
			wantNotIn: []string{"iVBORw0KGgo"},
		},
		{
			name:   "leaves ordinary markup, attributes, and text untouched",
			in:     `<div class="hero" onclick="doThing()"><h1>Real Title</h1><p>Some real text content.</p></div>`,
			wantIn: []string{`<div class="hero" onclick="doThing()">`, "<h1>Real Title</h1>", "Some real text content."},
		},
		{
			// A "bundled" single-file HTML export (see e.g. a downloaded
			// Claude Artifact) stores its actual page content inside
			// non-executable <script type="..."> data islands, read by
			// its own unpacking script rather than run by the browser —
			// stripping every <script> regardless of type used to delete
			// this along with real executable scripts, leaving nothing
			// but the export's loading-screen markup for the model to
			// read.
			name: "keeps a non-executable script type's content (JSON/template data island)",
			in: `<div id="loading">Unpacking...</div>` +
				`<script type="application/json">{"title":"Real Homepage Title","price":"£29.95"}</script>` +
				`<script type="__bundler/template">"<h1>Numbing your clinic can plan around.</h1>"</script>`,
			wantIn: []string{
				`<script type="application/json">{"title":"Real Homepage Title","price":"£29.95"}</script>`,
				`<script type="__bundler/template">"<h1>Numbing your clinic can plan around.</h1>"</script>`,
			},
		},
		{
			name:      "still strips an executable script even when quoted with single quotes or an unquoted type",
			in:        `<script type='module'>doEvil()</script><script type=text/javascript>alsoEvil()</script>`,
			wantNotIn: []string{"doEvil", "alsoEvil"},
		},
		{
			// The real-world case that broke: a kept non-executable script
			// block (an asset manifest) is exactly where a "bundled" export
			// puts its embedded fonts/JS libraries as raw base64 — no
			// data: URI prefix for dataURIRe to key off. Left alone, that
			// payload alone runs into the hundreds of KB and blows straight
			// through MaxHTMLAttachmentBytes, turning "the model can now
			// read this file" back into an outright rejected attachment.
			name: "strips a long base64 asset blob inside a kept non-executable script even without a data: URI prefix",
			in: `<script type="__bundler/manifest">{"font-uuid":{"mime":"font/woff2","data":"` +
				strings.Repeat("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU2Nzg5", 40) +
				`"}}</script><script type="__bundler/template">"<h1>Real Title</h1>"</script>`,
			wantIn:    []string{`<script type="__bundler/template">"<h1>Real Title</h1>"</script>`, `"font-uuid":{"mime":"font/woff2","data":""}`},
			wantNotIn: []string{"QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU2Nzg5QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU2Nzg5"},
		},
		{
			name:      "strips an inline svg icon",
			in:        `<button><svg viewBox="0 0 24 24"><path d="M12 2L2 22h20z" fill="#f00"/></svg>Menu</button>`,
			wantIn:    []string{"Menu"},
			wantNotIn: []string{"<svg", "viewBox", "M12 2L2 22h20z"},
		},
		{
			name:      "strips a self-closing svg",
			in:        `<p>before</p><svg viewBox="0 0 1 1" /><p>after</p>`,
			wantNotIn: []string{"<svg", "viewBox"},
		},
		{
			name:   "empty input stays empty",
			in:     "",
			wantIn: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeHTMLAttachment(tt.in)
			for _, want := range tt.wantIn {
				if !strings.Contains(got, want) {
					t.Errorf("expected output to contain %q, got: %s", want, got)
				}
			}
			for _, notWant := range tt.wantNotIn {
				if strings.Contains(got, notWant) {
					t.Errorf("expected output to NOT contain %q, got: %s", notWant, got)
				}
			}
		})
	}
}

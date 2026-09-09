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

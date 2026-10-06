package imageplacement

import (
	"bytes"
	"strings"
	"testing"
)

var (
	pngBytes  = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	jpegBytes = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'}
	webpBytes = []byte("RIFF\x24\x00\x00\x00WEBPVP8 ")
	svgBytes  = []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
)

func TestSniff(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		wantExt string
		wantOK  bool
	}{
		{"png", pngBytes, ".png", true},
		{"jpeg", jpegBytes, ".jpg", true},
		{"webp", webpBytes, ".webp", true},
		{"svg is never an image here", svgBytes, "", false},
		{"riff but not webp", []byte("RIFF\x24\x00\x00\x00WAVEfmt "), "", false},
		{"plain text", []byte("hello"), "", false},
		{"empty", nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Sniff(tt.data)
			if ok != tt.wantOK || got.Ext != tt.wantExt {
				t.Errorf("Sniff() = (%q, %v), want (%q, %v)", got.Ext, ok, tt.wantExt, tt.wantOK)
			}
		})
	}
}

func TestReference_RoundTrip(t *testing.T) {
	id, ok := ParseReference(Reference("att-123"))
	if !ok || id != "att-123" {
		t.Fatalf("ParseReference(Reference) = (%q, %v), want (att-123, true)", id, ok)
	}
	for _, content := range []string{"", "attachment:", "<div>attachment:x</div>", "iVBORw0KGgo="} {
		if _, ok := ParseReference(content); ok {
			t.Errorf("ParseReference(%q) = ok, want not a reference", content)
		}
	}
}

func TestCheck(t *testing.T) {
	oversized := append(append([]byte(nil), jpegBytes...), bytes.Repeat([]byte{0}, DefaultMaxBytes)...)
	tests := []struct {
		name      string
		placement Placement
		data      []byte
		found     bool
		pathTaken bool
		wantErr   string // substring; "" means accepted
	}{
		{"jpeg as jpg", Placement{1, "images/hero.jpg"}, jpegBytes, true, false, ""},
		{"png", Placement{1, "images/hero.png"}, pngBytes, true, false, ""},
		{"webp in a subfolder", Placement{2, "images/about/team.webp"}, webpBytes, true, false, ""},
		{"jpeg named png", Placement{1, "images/hero.png"}, jpegBytes, true, false, "must end in .jpg"},
		{"png named webp", Placement{1, "images/hero.webp"}, pngBytes, true, false, "must end in .png"},
		{"svg path", Placement{1, "images/logo.svg"}, svgBytes, true, false, "SVG can't be placed"},
		{"svg bytes under a png name", Placement{1, "images/logo.png"}, svgBytes, true, false, "not a PNG, JPEG or WebP"},
		{"non-image", Placement{1, "images/notes.png"}, []byte("just some text"), true, false, "not a PNG, JPEG or WebP"},
		{"oversized", Placement{1, "images/big.jpg"}, oversized, true, false, "over the 2 MB limit"},
		{"existing path", Placement{1, "images/hero.jpg"}, jpegBytes, true, true, "already exists"},
		{"unknown attachment number", Placement{7, "images/hero.jpg"}, nil, false, false, "no attached image 7"},
		{".jpeg extension", Placement{1, "images/hero.jpeg"}, jpegBytes, true, false, "not accept .jpeg"},
		{"outside images/", Placement{1, "assets/hero.jpg"}, jpegBytes, true, false, "under images/"},
		{"images/ itself", Placement{1, "images/"}, jpegBytes, true, false, "under images/"},
		{"path traversal", Placement{1, "images/../pages/x.jpg"}, jpegBytes, true, false, "no '..'"},
		{"space in name", Placement{1, "images/my hero.jpg"}, jpegBytes, true, false, "may only use"},
		{"other extension", Placement{1, "images/hero.gif"}, jpegBytes, true, false, "only .png, .jpg or .webp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Check(tt.placement, tt.data, tt.found, tt.pathTaken, DefaultMaxBytes)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Check() = %v, want accepted", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Check() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestCheck_LimitIsConfigurable(t *testing.T) {
	data := append(append([]byte(nil), jpegBytes...), bytes.Repeat([]byte{0}, 3*1024*1024)...)
	p := Placement{1, "images/photo.jpg"}
	if err := Check(p, data, true, false, 5*1024*1024); err != nil {
		t.Fatalf("a 3 MB image under a 5 MB limit must pass, got %v", err)
	}
	if err := Check(p, data, true, false, 2*1024*1024); err == nil || !strings.Contains(err.Error(), "3.0 MB, over the 2 MB limit") {
		t.Fatalf("a 3 MB image under a 2 MB limit must fail with both sizes, got %v", err)
	}
}

func TestFormatSize(t *testing.T) {
	for n, want := range map[int]string{2 * 1024 * 1024: "2 MB", 1536 * 1024: "1.5 MB", 100 * 1024: "0.1 MB"} {
		if got := FormatSize(n); got != want {
			t.Errorf("FormatSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestIsReferenced(t *testing.T) {
	tests := []struct {
		name  string
		texts []string
		want  bool
	}{
		{"liquid asset_url", []string{`<img src="{{ 'images/hero.jpg' | asset_url }}">`}, true},
		{"css url()", []string{`.hero { background: url("{{ 'images/hero.jpg' | asset_url }}"); }`}, true},
		{"plain css url", []string{`.hero { background-image: url(/images/hero.jpg); }`}, true},
		{"second of several files", []string{"<h1>Hi</h1>", `url(images/hero.jpg)`}, true},
		{"inside a comment still counts", []string{`{% comment %}images/hero.jpg{% endcomment %}`}, true},
		{"a different image", []string{`{{ 'images/home-hero.jpg' | asset_url }}`}, false},
		{"no files", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsReferenced("images/hero.jpg", tt.texts); got != tt.want {
				t.Errorf("IsReferenced() = %v, want %v", got, tt.want)
			}
		})
	}
}

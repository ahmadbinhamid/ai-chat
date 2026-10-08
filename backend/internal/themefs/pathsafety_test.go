package themefs

import (
	"strings"
	"testing"
)

func TestValidatePathSafety_Allowed(t *testing.T) {
	for _, p := range []string{
		"pages/offers.liquid",
		"pages/css/offers.css",
		"js/offers-filter.js",
		"components/header.liquid",
		// Extension-agnostic: internal reads/writes of pages.json/defaults.json must pass this check.
		"pages.json",
		"defaults.json",
	} {
		if err := ValidatePathSafety(p); err != nil {
			t.Errorf("expected %q to be allowed, got error: %v", p, err)
		}
	}
}

func TestValidatePathSafety_Rejected(t *testing.T) {
	for _, p := range []string{
		"",
		"/etc/passwd",
		"../../../etc/passwd",
		"pages/../../../etc/passwd",
		"pages\\offers.liquid",
		"pages/offers.liquid/../../secret.js",
	} {
		if err := ValidatePathSafety(p); err == nil {
			t.Errorf("expected %q to be rejected, got no error", p)
		}
	}
}

func TestValidateGeneratedFilePath_Allowed(t *testing.T) {
	for _, p := range []string{
		"pages/offers.liquid",
		"pages/css/offers.css",
		"js/offers-filter.js",
		"components/header.liquid",
		// .json is a generally allowed extension — pages.json/defaults.json and component-scoped configs all qualify.
		"defaults.json",
		"pages.json",
		"components/some-widget.json",
		// A known, singular theme-root file with no matching extension —
		// see allowedGeneratedFullPaths.
		"robots.txt",
	} {
		if err := ValidateGeneratedFilePath(p); err != nil {
			t.Errorf("expected %q to be allowed, got error: %v", p, err)
		}
	}
}

func TestValidateGeneratedFilePath_Rejected(t *testing.T) {
	for _, p := range []string{
		"",
		"/etc/passwd",
		"../../../etc/passwd",
		"pages/../../../etc/passwd",
		"pages\\offers.liquid",
		"pages/offers.liquid/../../secret.js",
		"pages/offers", // no extension
		// A different tech stack entirely — the allowlist's real job: every real theme file kind is allowed, nothing outside that vocabulary ever will be.
		"pages/offers.php",
		"components/Widget.jsx",
		"scripts/build.py",
		// robots.txt is allowed by exact path (see allowedGeneratedFullPaths)
		// — a same-named file elsewhere in the tree is not the same thing.
		"pages/robots.txt",
	} {
		if err := ValidateGeneratedFilePath(p); err == nil {
			t.Errorf("expected %q to be rejected, got no error", p)
		}
	}
}

func TestValidateThemeSlug(t *testing.T) {
	if err := ValidateThemeSlug("acmestore"); err != nil {
		t.Errorf("expected a plain slug to be valid, got: %v", err)
	}
	for _, slug := range []string{"", "../escape", "a/b", "a\\b"} {
		if err := ValidateThemeSlug(slug); err == nil {
			t.Errorf("expected slug %q to be rejected, got no error", slug)
		}
	}
}

// The observed production rejection: an image asset must fail with the specific extension message, listing what is allowed.
func TestValidateGeneratedFilePath_ImageRejectedWithSpecificMessage(t *testing.T) {
	tests := []struct {
		path string
		ext  string
	}{
		{"images/coffee-hero.svg", `".svg"`},
		{"images/banner.png", `".png"`},
		{"images/photo.jpg", `".jpg"`},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			err := ValidateGeneratedFilePath(tt.path)
			if err == nil {
				t.Fatalf("expected %q to be rejected", tt.path)
			}
			for _, want := range []string{tt.ext, "is not allowed for a generated file", GeneratedFileTypes()} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err.Error(), want)
				}
			}
		})
	}
}

func TestGeneratedFileTypes(t *testing.T) {
	if got, want := GeneratedFileTypes(), ".css, .js, .json, .liquid, or exactly robots.txt at the theme root"; got != want {
		t.Errorf("GeneratedFileTypes() = %q, want %q", got, want)
	}
}

func TestValidateThemeSlug_DotSegments(t *testing.T) {
	tests := []struct {
		slug    string
		wantErr bool
	}{
		{".", true},
		{"..", true},
		{"...", false}, // not a path segment of its own; treated like any other name
		{"shop.v2", false},
		{"..shop", false},
	}
	for _, tt := range tests {
		t.Run(tt.slug, func(t *testing.T) {
			if err := ValidateThemeSlug(tt.slug); (err != nil) != tt.wantErr {
				t.Errorf("ValidateThemeSlug(%q) error = %v, wantErr %v", tt.slug, err, tt.wantErr)
			}
		})
	}
}

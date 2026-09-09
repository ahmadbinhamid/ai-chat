package themefs

import "testing"

func TestValidatePathSafety_Allowed(t *testing.T) {
	for _, p := range []string{
		"pages/offers.liquid",
		"pages/css/offers.css",
		"js/offers-filter.js",
		"components/header.liquid",
		// Extension-agnostic: internal reads/writes of known config files
		// (pages.json, defaults.json) must pass this check — only
		// ValidateGeneratedFilePath restricts extensions.
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
		// .json is a generally allowed extension (see
		// allowedGeneratedExtensions) — pages.json and defaults.json are
		// both real .json files, no separate carve-out needed for either
		// anymore; a component-scoped config file works the same way.
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
		// A different tech stack entirely — the allowlist's real job (see
		// allowedGeneratedExtensions's own doc comment): every real theme
		// file kind is allowed now, but nothing outside that vocabulary
		// ever will be.
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

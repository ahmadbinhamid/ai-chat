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
		// A known, singular config file — see allowedGeneratedFullPaths —
		// allowed by exact path despite its .json extension, unlike
		// pages.json (still rejected below: structural merge only).
		"defaults.json",
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
		// pages.json must go through a register_page apply action's
		// structural merge, never a raw AI-generated file write — unlike
		// defaults.json (see TestValidateGeneratedFilePath_Allowed), which
		// has no such merge step and is safe to overwrite wholesale.
		"pages.json",
		"pages/offers.liquid/../../secret.js",
		"pages/offers", // no extension
		"pages/offers.php",
	} {
		if err := ValidateGeneratedFilePath(p); err == nil {
			t.Errorf("expected %q to be rejected, got no error", p)
		}
	}
}

func TestValidateThemeSlug(t *testing.T) {
	if err := ValidateThemeSlug("jpronumbingcream"); err != nil {
		t.Errorf("expected a plain slug to be valid, got: %v", err)
	}
	for _, slug := range []string{"", "../escape", "a/b", "a\\b"} {
		if err := ValidateThemeSlug(slug); err == nil {
			t.Errorf("expected slug %q to be rejected, got no error", slug)
		}
	}
}

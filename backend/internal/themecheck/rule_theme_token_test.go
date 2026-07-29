package themecheck

import "testing"

func TestCheckThemeToken_ValidTokenWithFallback(t *testing.T) {
	content := ".x { color: var(--theme-primary, #1e3a8a); background: var(--theme-bg, #fff); }"
	p := Proposal{Files: []ProposedFile{{Path: "components/css/testimonials.css", Content: content}}}
	if got := checkThemeToken(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckThemeToken_RawHexOnColorProperty(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "components/css/testimonials.css", Content: ".x { color: #1e3a8a; }"}}}
	got := checkThemeToken(p, Snapshot{})
	if len(got) != 1 || got[0].Severity != SeverityError {
		t.Fatalf("expected 1 error finding, got %+v", got)
	}
}

func TestCheckThemeToken_RawRGBOnBackground(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "components/css/testimonials.css", Content: ".x { background-color: rgb(30, 58, 138); }"}}}
	got := checkThemeToken(p, Snapshot{})
	if len(got) != 1 || got[0].Severity != SeverityError {
		t.Fatalf("expected 1 error finding, got %+v", got)
	}
}

func TestCheckThemeToken_HexInsideVarFallbackIsFine(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "components/css/testimonials.css", Content: ".x { border-color: var(--theme-border, #e5e5e5); }"}}}
	if got := checkThemeToken(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings for a hex fallback inside var(), got %+v", got)
	}
}

func TestCheckThemeToken_HexInCustomPropertyIsWarning(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "components/css/testimonials.css", Content: ".x { --testimonials-accent: #ff6600; }"}}}
	got := checkThemeToken(p, Snapshot{})
	if len(got) != 1 || got[0].Severity != SeverityWarning {
		t.Fatalf("expected 1 warning finding, got %+v", got)
	}
}

func TestCheckThemeToken_ThemeVarMissingFallback(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "components/css/testimonials.css", Content: ".x { color: var(--theme-primary); }"}}}
	got := checkThemeToken(p, Snapshot{})
	// Fires twice: once for the missing-fallback var() itself, and once
	// more because after stripping the var() call the color-property check
	// no longer sees a raw hex, so only the fallback finding is expected.
	if len(got) != 1 || got[0].Severity != SeverityError {
		t.Fatalf("expected 1 error finding for a missing fallback, got %+v", got)
	}
}

func TestCheckThemeToken_NonColorPropertyIgnored(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "components/css/testimonials.css", Content: ".x { box-shadow: 0 0 4px #000; }"}}}
	if got := checkThemeToken(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected box-shadow to be out of scope for rule 8, got %+v", got)
	}
}

func TestCheckThemeToken_IgnoresNonCSSFiles(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: "color: #fff;"}}}
	if got := checkThemeToken(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected non-.css files to be ignored, got %+v", got)
	}
}

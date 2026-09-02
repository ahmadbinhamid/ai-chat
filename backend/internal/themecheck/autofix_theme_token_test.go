package themecheck

import "testing"

const testDefaultsJSON = `{"colors": {
	"primary": "#1e3a8a",
	"secondary": "#111111",
	"accent": "#3d5bbf",
	"background": "#ffffff",
	"border": "#e8e8e8",
	"danger": "#dc2626"
}}`

func TestAutoFixThemeTokens_AddsFallbackFromDefaultsJSON(t *testing.T) {
	content := "a { color: var(--theme-primary); }\n"
	p := Proposal{Files: []ProposedFile{{Path: "components/css/x.css", Action: "create", Content: content}}}
	snap := Snapshot{Files: map[string]string{"defaults.json": testDefaultsJSON}}

	findings := checkThemeToken(p, snap)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %+v", findings)
	}

	fixed, any := AutoFixThemeTokens(p, snap, findings)
	if !any {
		t.Fatal("expected a fix")
	}
	want := "a { color: var(--theme-primary, #1e3a8a); }\n"
	if got := fixed["components/css/x.css"]; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAutoFixThemeTokens_RawHexBecomesToken(t *testing.T) {
	content := "a { color: #1e3a8a; }\n"
	p := Proposal{Files: []ProposedFile{{Path: "components/css/x.css", Action: "create", Content: content}}}
	snap := Snapshot{Files: map[string]string{"defaults.json": testDefaultsJSON}}

	findings := checkThemeToken(p, snap)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %+v", findings)
	}

	fixed, any := AutoFixThemeTokens(p, snap, findings)
	if !any {
		t.Fatal("expected a fix")
	}
	want := "a { color: var(--theme-primary, #1e3a8a); }\n"
	if got := fixed["components/css/x.css"]; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestAutoFixThemeTokens_RawHexUsesKebabCaseTokenName confirms the
// camelCase (defaults.json) <-> kebab-case (--theme-*) conversion, not just
// the trivial single-word case the other tests use.
func TestAutoFixThemeTokens_RawHexUsesKebabCaseTokenName(t *testing.T) {
	defaultsJSON := `{"colors": {"footerBg": "#03318f"}}`
	content := "a { background-color: #03318f; }\n"
	p := Proposal{Files: []ProposedFile{{Path: "components/css/x.css", Action: "create", Content: content}}}
	snap := Snapshot{Files: map[string]string{"defaults.json": defaultsJSON}}

	findings := checkThemeToken(p, snap)
	fixed, any := AutoFixThemeTokens(p, snap, findings)
	if !any {
		t.Fatal("expected a fix")
	}
	want := "a { background-color: var(--theme-footer-bg, #03318f); }\n"
	if got := fixed["components/css/x.css"]; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAutoFixThemeTokens_UnknownColorLeftAlone(t *testing.T) {
	content := "a { color: #ff00ff; }\n"
	p := Proposal{Files: []ProposedFile{{Path: "components/css/x.css", Action: "create", Content: content}}}
	snap := Snapshot{Files: map[string]string{"defaults.json": testDefaultsJSON}}

	findings := checkThemeToken(p, snap)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %+v", findings)
	}

	if _, any := AutoFixThemeTokens(p, snap, findings); any {
		t.Fatal("expected no fix for a color matching no known token — never guess")
	}
	// Still flagged — nothing silently swallowed it.
	if again := checkThemeToken(p, snap); len(again) != 1 || again[0].Severity != SeverityError {
		t.Errorf("expected the unfixable finding to remain an error, got %+v", again)
	}
}

// TestAutoFixThemeTokens_GrandfatheredDeclarationNotRewritten is the scope
// test this whole task hinges on: checkThemeToken never flags a
// declaration byte-identical to the file's pre-edit content, so
// AutoFixThemeTokens — which only acts on flagged lines — must never touch
// it either, even though it contains a raw color that would otherwise be
// fixable.
func TestAutoFixThemeTokens_GrandfatheredDeclarationNotRewritten(t *testing.T) {
	prevContent := "a { color: #1e3a8a; }\n"
	p := Proposal{Files: []ProposedFile{{Path: "components/css/x.css", Action: "update", Content: prevContent}}}
	snap := Snapshot{Files: map[string]string{
		"defaults.json":        testDefaultsJSON,
		"components/css/x.css": prevContent, // pre-edit content, byte-identical
	}}

	findings := checkThemeToken(p, snap)
	if len(findings) != 0 {
		t.Fatalf("expected the grandfathered declaration to produce no findings, got %+v", findings)
	}

	if _, any := AutoFixThemeTokens(p, snap, findings); any {
		t.Fatal("expected no fix — nothing was flagged, so nothing should be touched")
	}
}

// TestAutoFixThemeTokens_PreservesRestOfShorthandValue confirms a
// byte-range replacement, not a global substitution — the url(...) part of
// a shorthand background declaration must survive untouched.
func TestAutoFixThemeTokens_PreservesRestOfShorthandValue(t *testing.T) {
	content := "a { background: #fff url('images/x.png') no-repeat; }\n"
	p := Proposal{Files: []ProposedFile{{Path: "components/css/x.css", Action: "create", Content: content}}}
	snap := Snapshot{Files: map[string]string{"defaults.json": testDefaultsJSON}}

	findings := checkThemeToken(p, snap)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %+v", findings)
	}

	fixed, any := AutoFixThemeTokens(p, snap, findings)
	if !any {
		t.Fatal("expected a fix")
	}
	want := "a { background: var(--theme-background, #fff) url('images/x.png') no-repeat; }\n"
	if got := fixed["components/css/x.css"]; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAutoFixThemeTokens_CustomPropertyWarningUntouched(t *testing.T) {
	content := ".x { --testimonials-accent: #1e3a8a; }\n"
	p := Proposal{Files: []ProposedFile{{Path: "components/css/x.css", Action: "create", Content: content}}}
	snap := Snapshot{Files: map[string]string{"defaults.json": testDefaultsJSON}}

	findings := checkThemeToken(p, snap)
	if len(findings) != 1 || findings[0].Severity != SeverityWarning {
		t.Fatalf("expected 1 warning finding, got %+v", findings)
	}

	if _, any := AutoFixThemeTokens(p, snap, findings); any {
		t.Fatal("expected no fix — warnings are left entirely alone, per rule 9's own allowance")
	}
}

func TestAutoFixThemeTokens_MissingDefaultsJSONFixesNothing(t *testing.T) {
	content := "a { color: #1e3a8a; }\n"
	p := Proposal{Files: []ProposedFile{{Path: "components/css/x.css", Action: "create", Content: content}}}
	snap := Snapshot{} // no defaults.json at all

	findings := checkThemeToken(p, snap)
	if _, any := AutoFixThemeTokens(p, snap, findings); any {
		t.Fatal("expected no fix when defaults.json is missing")
	}
}

func TestAutoFixThemeTokens_UnparseableDefaultsJSONFixesNothing(t *testing.T) {
	content := "a { color: #1e3a8a; }\n"
	p := Proposal{Files: []ProposedFile{{Path: "components/css/x.css", Action: "create", Content: content}}}
	snap := Snapshot{Files: map[string]string{"defaults.json": "{not valid json"}}

	findings := checkThemeToken(p, snap)
	if _, any := AutoFixThemeTokens(p, snap, findings); any {
		t.Fatal("expected no fix when defaults.json is unparseable")
	}
}

// TestAutoFixThemeTokens_DeclarationHittingBothFixTypes covers the
// combined edge case: a var() with no fallback AND a raw hex elsewhere in
// the SAME declaration. Both must be fixed, and the second fix's byte
// offset must still land correctly after the first fix changed the
// content's length.
func TestAutoFixThemeTokens_DeclarationHittingBothFixTypes(t *testing.T) {
	content := "a { border-color: var(--theme-primary) #dc2626; }\n"
	p := Proposal{Files: []ProposedFile{{Path: "components/css/x.css", Action: "create", Content: content}}}
	snap := Snapshot{Files: map[string]string{"defaults.json": testDefaultsJSON}}

	findings := checkThemeToken(p, snap)
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings (no-fallback + raw color), got %d: %+v", len(findings), findings)
	}

	fixed, any := AutoFixThemeTokens(p, snap, findings)
	if !any {
		t.Fatal("expected fixes to be applied")
	}
	want := "a { border-color: var(--theme-primary, #1e3a8a) var(--theme-danger, #dc2626); }\n"
	if got := fixed["components/css/x.css"]; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestAutoFixThemeTokens_SixFindingsAllFixableEndToEnd is the case that
// motivates the whole task: six hardcoded colors in one file, all
// resolvable against defaults.json, come back with zero findings on the
// re-run Check — the repair round-trip is skipped entirely.
func TestAutoFixThemeTokens_SixFindingsAllFixableEndToEnd(t *testing.T) {
	content := `.a { color: #1e3a8a; }
.b { color: #111111; }
.c { color: #3d5bbf; }
.d { background-color: #ffffff; }
.e { border-color: #e8e8e8; }
.f { color: #dc2626; }
`
	p := Proposal{Files: []ProposedFile{{Path: "components/css/x.css", Action: "create", Content: content}}}
	snap := Snapshot{Files: map[string]string{"defaults.json": testDefaultsJSON}}

	findings := checkThemeToken(p, snap)
	if len(findings) != 6 {
		t.Fatalf("expected 6 findings, got %d: %+v", len(findings), findings)
	}

	fixedContent, any := AutoFixThemeTokens(p, snap, findings)
	if !any {
		t.Fatal("expected fixes to be applied")
	}
	p.Files[0].Content = fixedContent["components/css/x.css"]

	remaining := checkThemeToken(p, snap)
	if len(remaining) != 0 {
		t.Fatalf("expected zero findings after auto-fix (the repair round-trip this feature exists to skip), got %+v", remaining)
	}
}

func TestKebabToCamel(t *testing.T) {
	cases := map[string]string{
		"primary":       "primary",
		"footer-bg":     "footerBg",
		"header-nav-bg": "headerNavBg",
	}
	for in, want := range cases {
		if got := kebabToCamel(in); got != want {
			t.Errorf("kebabToCamel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCamelToKebab(t *testing.T) {
	cases := map[string]string{
		"primary":     "primary",
		"footerBg":    "footer-bg",
		"headerNavBg": "header-nav-bg",
	}
	for in, want := range cases {
		if got := camelToKebab(in); got != want {
			t.Errorf("camelToKebab(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeOpaqueHex(t *testing.T) {
	cases := []struct {
		in      string
		wantHex string
		wantOK  bool
	}{
		{"#FFF", "#ffffff", true},
		{"#ffffff", "#ffffff", true},
		{"#FFFFFF", "#ffffff", true},
		{"#1e3a8a", "#1e3a8a", true},
		{"#1E3A8A", "#1e3a8a", true},
		{"#ffffffaa", "", false}, // 8-digit, carries alpha
		{"#ffff", "", false},     // 4-digit, carries alpha
		{"rgba(0,0,0,0.5)", "", false},
	}
	for _, tt := range cases {
		got, ok := normalizeOpaqueHex(tt.in)
		if ok != tt.wantOK || got != tt.wantHex {
			t.Errorf("normalizeOpaqueHex(%q) = (%q, %v), want (%q, %v)", tt.in, got, ok, tt.wantHex, tt.wantOK)
		}
	}
}

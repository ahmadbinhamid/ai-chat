package themecheck

import "testing"

// The forms the system prompt's rule 12 tells the model to use for translucent colours must pass rule 8, and the raw
// forms Flash kept writing (from the rejected proposals' findings) must not.
func TestCheckThemeToken_PromptRule12Forms(t *testing.T) {
	tests := []struct {
		name       string
		css        string
		wantErrors int
	}{
		{"component token declared and used with a fallback",
			".site-header { --header-overlay: rgba(255, 255, 255, 0.08); background: var(--header-overlay, rgba(255, 255, 255, 0.08)); }", 0},
		{"theme token with a fallback", ".site-header { color: var(--theme-text, #ffffff); }", 0},
		{"tokens inside a gradient",
			".site-header { background: linear-gradient(180deg, var(--header-glow, rgba(255, 255, 255, 0.16)) 0%, transparent 100%); }", 0},
		{"raw translucent background", ".site-header { background: rgba(255, 255, 255, 0.08); }", 1},
		{"raw translucent text colour", ".nav a { color: rgba(255, 255, 255, 0.72); }", 1},
		{"raw colour inside a gradient",
			".site-header { background: linear-gradient(180deg, rgba(255, 255, 255, 0.16) 0%, transparent 100%); }", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Proposal{Files: []ProposedFile{{Path: "components/css/header.css", Action: "create", Content: tt.css}}}
			errors := 0
			for _, f := range checkThemeToken(p, Snapshot{}) {
				if f.Severity == SeverityError {
					errors++
				}
			}
			if errors != tt.wantErrors {
				t.Errorf("got %d errors, want %d", errors, tt.wantErrors)
			}
		})
	}
}

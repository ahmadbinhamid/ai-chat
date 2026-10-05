package themecheck

import (
	"strings"
	"testing"
)

const savedHeaderCSS = `.header {
  display: flex;
  padding: 16px;
}
`

// An earlier pending turn ("make the header background dark") added four declarations.
const draftHeaderCSS = `.header {
  display: flex;
  padding: 16px;
  background: var(--color-dark);
  color: var(--color-light);
  border-bottom: 1px solid var(--color-border);
  box-shadow: 0 2px 4px rgba(0, 0, 0, 0.2);
}
`

func TestDetectDraftReversions(t *testing.T) {
	path := "components/css/header.css"
	tests := []struct {
		name     string
		action   string
		proposed string
		wantHit  bool
	}{
		{"update reverting to the saved file", "update", savedHeaderCSS, true},
		{"update dropping three of four added lines", "update",
			strings.Replace(savedHeaderCSS, "}", "  background: var(--color-dark);\n}", 1), true},
		{"edit replacing one added line", "update",
			strings.Replace(draftHeaderCSS, "color: var(--color-light);", "color: #fff;", 1), false},
		{"edit replacing two added lines", "update", strings.NewReplacer(
			"color: var(--color-light);", "color: white;",
			"background: var(--color-dark);", "background: var(--color-primary);").Replace(draftHeaderCSS), false},
		{"whitespace-only changes", "update", strings.NewReplacer("  ", "\t", ": ", ":   ").Replace(draftHeaderCSS), false},
		{"moved lines", "update", `.header {
  box-shadow: 0 2px 4px rgba(0, 0, 0, 0.2);
  border-bottom: 1px solid var(--color-border);
  color: var(--color-light);
  background: var(--color-dark);
  padding: 16px;
  display: flex;
}
`, false},
		{"unrelated additive change", "update", draftHeaderCSS + ".header a { color: inherit; }\n", false},
		{"create is never compared", "create", savedHeaderCSS, false},
	}
	saved := map[string]string{path: savedHeaderCSS}
	draft := map[string]string{path: draftHeaderCSS}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Proposal{Files: []ProposedFile{{Path: path, Action: tt.action, Content: tt.proposed}}}
			got := DetectDraftReversions(p, saved, draft)
			if (len(got) > 0) != tt.wantHit {
				t.Fatalf("hit = %v, want %v (%+v)", len(got) > 0, tt.wantHit, got)
			}
			if tt.wantHit && (got[0].Added != 4 || got[0].Dropped < draftReversionMinDropped) {
				t.Errorf("unexpected counts %+v", got[0])
			}
		})
	}
}

func TestDetectDraftReversions_SkipsFilesNotInDraftOrSaved(t *testing.T) {
	p := Proposal{Files: []ProposedFile{
		{Path: "a.css", Action: "update", Content: ""},
		{Path: "b.css", Action: "update", Content: ""},
	}}
	got := DetectDraftReversions(p, map[string]string{"b.css": savedHeaderCSS}, map[string]string{"a.css": draftHeaderCSS})
	if len(got) != 0 {
		t.Errorf("expected no reversions without both a saved and a draft version, got %+v", got)
	}
}

// Detection is never part of Check, and its finding is a warning, so it can't reject a proposal.
func TestDraftReversion_NeverRejects(t *testing.T) {
	path := "components/css/header.css"
	p := Proposal{Files: []ProposedFile{{Path: path, Action: "update", Content: savedHeaderCSS}}}
	for _, f := range Check(p, Snapshot{Files: map[string]string{path: draftHeaderCSS}}) {
		if f.Rule == ruleIDDraftReversion {
			t.Errorf("Check must not run draft-reversion detection, got %+v", f)
		}
	}
	got := DetectDraftReversions(p, map[string]string{path: savedHeaderCSS}, map[string]string{path: draftHeaderCSS})
	if len(got) != 1 {
		t.Fatalf("expected a reversion, got %+v", got)
	}
	f := got[0].Finding()
	if f.Severity != SeverityWarning || f.Rule != ruleIDDraftReversion || f.Path != path {
		t.Errorf("unexpected finding %+v", f)
	}
	if strings.Contains(f.Message, "line") || strings.Contains(f.Message, "{") {
		t.Errorf("merchant-facing message should be plain language, got %q", f.Message)
	}
}

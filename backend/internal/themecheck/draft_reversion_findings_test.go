package themecheck

import (
	"strings"
	"testing"
)

func TestDraftReversionFindings(t *testing.T) {
	path := "components/css/header.css"
	// The add-to-cart turn rebuilt header.css from the saved version plus its own tweak, dropping the dark header.
	rewritten := strings.Replace(savedHeaderCSS, "padding: 16px;", "padding: 16px;\n  z-index: 10;", 1)
	oneLineEdit := strings.Replace(draftHeaderCSS, "padding: 16px;", "padding: 20px;", 1)

	tests := []struct {
		name        string
		proposed    string
		wantBlocked bool
	}{
		{name: "rewrite dropping the earlier turn's lines is blocked", proposed: rewritten, wantBlocked: true},
		{name: "one-line edit keeps the earlier work", proposed: oneLineEdit},
		{name: "proposal keeping every earlier line", proposed: draftHeaderCSS + ".header a { color: inherit; }\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Proposal{Files: []ProposedFile{{Path: path, Action: "update", Content: tt.proposed}}}
			got := DraftReversionFindings(p, map[string]string{path: savedHeaderCSS}, map[string]string{path: draftHeaderCSS})
			if !tt.wantBlocked {
				if len(got) != 0 {
					t.Fatalf("expected no findings, got %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Path != path || got[0].Severity != SeverityError || got[0].Rule != ruleIDDraftReversion {
				t.Fatalf("expected one blocking finding for %s, got %+v", path, got)
			}
			msg := got[0].Message
			for _, want := range []string{
				"Your change to `" + path + "` removes earlier unsaved work the merchant hasn't asked to undo.",
				"background: var(--color-dark);",
				"box-shadow: 0 2px 4px rgba(0, 0, 0, 0.2);",
			} {
				if !strings.Contains(msg, want) {
					t.Errorf("message missing %q:\n%s", want, msg)
				}
			}
			if strings.Index(msg, "background:") > strings.Index(msg, "box-shadow:") {
				t.Errorf("dropped lines must be quoted in file order:\n%s", msg)
			}
		})
	}
}

func TestDraftReversionFindings_CapsQuotedLines(t *testing.T) {
	path := "components/css/footer.css"
	saved := ".footer {}\n"
	var b strings.Builder
	b.WriteString(saved)
	for i := 0; i < 12; i++ {
		b.WriteString(".footer-item-" + strings.Repeat("x", i+1) + " { margin: 0; }\n")
	}
	b.WriteString(".long { content: \"" + strings.Repeat("a", 400) + "\"; }\n")
	p := Proposal{Files: []ProposedFile{{Path: path, Action: "update", Content: saved}}}

	got := DraftReversionFindings(p, map[string]string{path: saved}, map[string]string{path: b.String()})
	if len(got) != 1 {
		t.Fatalf("expected one finding, got %+v", got)
	}
	if quoted := strings.Count(got[0].Message, "\n"); quoted != draftReversionQuotedLines {
		t.Errorf("expected %d quoted lines, got %d:\n%s", draftReversionQuotedLines, quoted, got[0].Message)
	}
	if strings.Contains(got[0].Message, ".long") {
		t.Error("lines past the cap must not be quoted")
	}
}

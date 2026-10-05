package themecheck

import (
	"strings"
	"testing"
)

// The click handler's closing brace is missing, so the parser hits ')' on line 6.
const brokenMinicart = `(function () {
  var root = document.querySelector('[data-minicart]');
  if (!root) return;
  root.addEventListener('click', function () {
    window.StorefrontApi.getBasket();
  );
})();
`

// The IIFE's closing brace is missing, so the error is reported at end of file, on a blank line.
const unclosedMinicart = "(function () {\n  var root = document.querySelector('[data-minicart]');\n  if (!root) return;\n"

func TestCheckJSSyntax(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		content  string
		wantLine int // 0 = no finding expected
	}{
		{"missing brace", "js/minicart.js", brokenMinicart, 6},
		{"unclosed function reported at end of file", "js/minicart.js", unclosedMinicart, 4},
		{"valid modern script", "js/product-grid-block.js",
			"(function () {\n  const items = [1, 2].map((x) => x * 2);\n  window.Grid = items?.[0] ?? 0;\n  import('./lazy.js');\n})();\n", 0},
		{"top-level await rejected in a classic script", "js/theme.js", "const r = await fetch('/api/basket');\n", 1},
		{"liquid is not parsed", "components/minicart.liquid", "{% if x %}<script>(function(){</script>{% endif %}", 0},
		{"css is not parsed", "components/css/header.css", ".header { color: red", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkJSSyntax(Proposal{Files: []ProposedFile{{Path: tt.path, Action: "update", Content: tt.content}}}, Snapshot{})
			if tt.wantLine == 0 {
				if len(got) != 0 {
					t.Fatalf("expected no findings, got %+v", got)
				}
				return
			}
			if len(got) == 0 {
				t.Fatal("expected a syntax finding, got none")
			}
			f := got[0]
			if f.Rule != ruleIDJSSyntax || f.Severity != SeverityError || f.Path != tt.path || f.Line != tt.wantLine {
				t.Errorf("unexpected finding %+v (want line %d)", f, tt.wantLine)
			}
			if wantPrefix := tt.path + " line "; !strings.HasPrefix(f.Message, wantPrefix) ||
				!strings.Contains(f.Message, "the file won't run at all until this is fixed") {
				t.Errorf("message not written for the model: %q", f.Message)
			}
		})
	}
}

func TestCheckJSSyntax_OffsetPointsAtError(t *testing.T) {
	got := checkJSSyntax(Proposal{Files: []ProposedFile{{Path: "js/minicart.js", Content: brokenMinicart}}}, Snapshot{})
	if len(got) == 0 {
		t.Fatal("expected a finding")
	}
	if want := strings.Index(brokenMinicart, "  );") + 2; got[0].Offset != want {
		t.Errorf("Offset = %d, want %d (start of the unexpected token)", got[0].Offset, want)
	}
}

func TestCheckJSSyntax_PreExistingErrorDowngraded(t *testing.T) {
	path := "js/minicart.js"
	valid := "(function () {})();\n"
	tests := []struct {
		name     string
		edited   string
		baseline string
		want     Severity
	}{
		{"mid-file error already on that line", "// tweak\n" + brokenMinicart, brokenMinicart, SeverityWarning},
		{"mid-file error newly introduced", "// tweak\n" + brokenMinicart, valid, SeverityError},
		{"end-of-file error in an already-broken file", "// tweak\n" + unclosedMinicart, unclosedMinicart, SeverityWarning},
		{"end-of-file error newly introduced", unclosedMinicart, valid, SeverityError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Proposal{Files: []ProposedFile{{Path: path, Action: "update", Content: tt.edited}}}
			baseline := map[string]string{path: tt.baseline}
			got := DowngradePreExistingFindings(checkJSSyntax(p, Snapshot{Files: baseline}), p, baseline)
			if len(got) == 0 || got[0].Severity != tt.want {
				t.Errorf("expected severity %q, got %+v", tt.want, got)
			}
		})
	}
}

func TestCheckJSSyntax_NewFileStaysError(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "js/new.js", Action: "create", Content: unclosedMinicart}}}
	got := DowngradePreExistingFindings(checkJSSyntax(p, Snapshot{}), p, map[string]string{})
	if len(got) == 0 || got[0].Severity != SeverityError {
		t.Errorf("expected a syntax error in a brand-new file to stay an error, got %+v", got)
	}
}

func TestCheck_RunsJSSyntax(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "js/minicart.js", Action: "update", Content: brokenMinicart}}}
	for _, f := range Check(p, Snapshot{}) {
		if f.Rule == ruleIDJSSyntax {
			return
		}
	}
	t.Error("expected Check to include the js-syntax rule")
}

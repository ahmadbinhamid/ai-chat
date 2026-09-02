package themecheck

import "testing"

// trustpilotScript is the exact kind of pre-existing off-theme <script src>
// the Trustpilot-widget incident this feature exists to prevent involved —
// see DowngradePreExistingFindings' own doc comment. Deliberately avoids the
// word "bootstrap" in the URL (the real widget's CDN path has one) — that
// would also trip frameworkSignals' unrelated Bootstrap-name-mention regex,
// muddying these tests with a second, incidental finding.
const trustpilotScript = `  <script src="https://widget.trustpilot.com/tp-widget.min.js"></script>`

func TestDowngradePreExistingFindings_PreExistingScriptBecomesWarning(t *testing.T) {
	baselineFooter := "<footer>\n" + trustpilotScript + "\n</footer>"
	// The model re-emits the whole file (proposals are always complete
	// files, never diffs) with the pre-existing script intact, plus its own
	// genuinely new line.
	proposedFooter := "<footer>\n  <p>Powered by FlowPOS</p>\n" + trustpilotScript + "\n</footer>"

	p := Proposal{Files: []ProposedFile{{Path: "components/footer.liquid", Action: "update", Content: proposedFooter}}}
	findings := checkNoFramework(p, Snapshot{})
	if len(findings) != 1 || findings[0].Severity != SeverityError {
		t.Fatalf("expected checkNoFramework to flag the script as an error before filtering, got %+v", findings)
	}
	if findings[0].Line == 0 {
		t.Fatalf("expected checkNoFramework to populate Line, got %+v", findings[0])
	}

	baseline := map[string]string{"components/footer.liquid": baselineFooter}
	got := DowngradePreExistingFindings(findings, p, baseline)

	if len(got) != 1 {
		t.Fatalf("expected the finding to survive (downgraded, not dropped), got %+v", got)
	}
	if got[0].Severity != SeverityWarning {
		t.Errorf("expected the pre-existing script to be downgraded to a warning, got %+v", got[0])
	}
	if got[0].Rule != ruleIDNoFramework || got[0].Message != findings[0].Message {
		t.Errorf("expected only Severity to change, got %+v (original %+v)", got[0], findings[0])
	}
}

func TestDowngradePreExistingFindings_NewFileStaysError(t *testing.T) {
	content := "<footer>\n" + trustpilotScript + "\n</footer>"
	p := Proposal{Files: []ProposedFile{{Path: "components/new-footer.liquid", Action: "create", Content: content}}}
	findings := checkNoFramework(p, Snapshot{})
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %+v", findings)
	}

	// No baseline entry at all for a brand-new file — the model wrote every
	// line of it, so it owns every violation.
	got := DowngradePreExistingFindings(findings, p, map[string]string{})

	if len(got) != 1 || got[0].Severity != SeverityError {
		t.Fatalf("expected a violation in a newly created file to stay an error, got %+v", got)
	}
}

func TestDowngradePreExistingFindings_ModelIntroducedViolationStaysError(t *testing.T) {
	baselineFooter := "<footer>\n  <p>Copyright 2024</p>\n</footer>"
	// The model's update adds a script the baseline never had — its own
	// mistake, not a pre-existing one.
	proposedFooter := "<footer>\n  <p>Copyright 2024</p>\n" + trustpilotScript + "\n</footer>"

	p := Proposal{Files: []ProposedFile{{Path: "components/footer.liquid", Action: "update", Content: proposedFooter}}}
	findings := checkNoFramework(p, Snapshot{})
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %+v", findings)
	}

	baseline := map[string]string{"components/footer.liquid": baselineFooter}
	got := DowngradePreExistingFindings(findings, p, baseline)

	if len(got) != 1 || got[0].Severity != SeverityError {
		t.Fatalf("expected a violation the model actually introduced to stay an error, got %+v", got)
	}
}

func TestDowngradePreExistingFindings_SurvivesLineShift(t *testing.T) {
	// The violating line sits at line 2 in the baseline.
	baselineFooter := "<footer>\n" + trustpilotScript + "\n</footer>"
	// The model inserts three new lines ABOVE it — in the proposal, the
	// same violating line is now line 5, not line 2. Index/line-number
	// comparison would call this "different"; content comparison must not.
	proposedFooter := "<footer>\n  <p>One</p>\n  <p>Two</p>\n  <p>Three</p>\n" + trustpilotScript + "\n</footer>"

	p := Proposal{Files: []ProposedFile{{Path: "components/footer.liquid", Action: "update", Content: proposedFooter}}}
	findings := checkNoFramework(p, Snapshot{})
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %+v", findings)
	}
	if findings[0].Line != 5 {
		t.Fatalf("test setup sanity check failed: expected the violation on line 5 after the insert, got line %d", findings[0].Line)
	}

	baseline := map[string]string{"components/footer.liquid": baselineFooter}
	got := DowngradePreExistingFindings(findings, p, baseline)

	if len(got) != 1 || got[0].Severity != SeverityWarning {
		t.Fatalf("expected the shifted-but-unchanged violation to still be classified pre-existing, got %+v", got)
	}
}

// TestDowngradePreExistingFindings_MissingBaselineStaysError models a
// baseline fetch failure (store error, network hiccup to FlowPOS) — from
// this function's point of view that's indistinguishable from a brand-new
// file (see themebuild.Service.buildSnapshot, which logs a Warn and simply
// omits the map entry rather than failing the generation): no entry means
// no baseline, which means stay strict.
func TestDowngradePreExistingFindings_MissingBaselineStaysError(t *testing.T) {
	content := "<footer>\n" + trustpilotScript + "\n</footer>"
	p := Proposal{Files: []ProposedFile{{Path: "components/footer.liquid", Action: "update", Content: content}}}
	findings := checkNoFramework(p, Snapshot{})
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %+v", findings)
	}

	got := DowngradePreExistingFindings(findings, p, map[string]string{}) // baseline fetch "failed" — no entry

	if len(got) != 1 || got[0].Severity != SeverityError {
		t.Fatalf("expected a missing baseline to fall back to error severity, got %+v", got)
	}
}

func TestDowngradePreExistingFindings_EdgeCases(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "components/footer.liquid", Action: "update", Content: "line one\nline two\n"}}}
	baseline := map[string]string{"components/footer.liquid": "line one\nline two\n"}

	tests := []struct {
		name     string
		finding  Finding
		wantSame bool // severity must be unchanged from input
	}{
		{
			name:     "Line == 0 stays an error even though the content matches",
			finding:  Finding{Path: "components/footer.liquid", Rule: "x", Severity: SeverityError, Message: "m", Line: 0},
			wantSame: true,
		},
		{
			name:     "theme-wide finding (Path == \"\") stays an error",
			finding:  Finding{Path: "", Rule: "x", Severity: SeverityError, Message: "m", Line: 1},
			wantSame: true,
		},
		{
			name:     "a warning is left alone, never downgraded to nothing or upgraded",
			finding:  Finding{Path: "components/footer.liquid", Rule: "x", Severity: SeverityWarning, Message: "m", Line: 1},
			wantSame: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DowngradePreExistingFindings([]Finding{tt.finding}, p, baseline)
			if len(got) != 1 {
				t.Fatalf("expected the finding to survive unconditionally, got %+v", got)
			}
			if got[0].Severity != tt.finding.Severity {
				t.Errorf("expected severity to stay %q, got %q", tt.finding.Severity, got[0].Severity)
			}
		})
	}
}

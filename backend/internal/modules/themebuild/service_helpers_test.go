package themebuild

import (
	"strings"
	"testing"

	"ai-chat/internal/ai"
	"ai-chat/internal/themecheck"
	"ai-chat/internal/themefs"
)

func TestToProposal(t *testing.T) {
	result := &ai.Result{
		Files:              []ai.GeneratedFile{{Path: "pages/offers.liquid", Action: "create", Content: "hi"}},
		PageRegistryEntry:  &themefs.PageEntry{Slug: "offers"},
		LayoutLinksToAdd:   []string{"pages/css/offers.css"},
		LayoutScriptsToAdd: []string{"js/offers.js"},
	}
	p := toProposal(result)
	if len(p.Files) != 1 || p.Files[0].Path != "pages/offers.liquid" || p.Files[0].Content != "hi" {
		t.Errorf("unexpected Files: %+v", p.Files)
	}
	if p.PageRegistryEntry != result.PageRegistryEntry {
		t.Errorf("expected PageRegistryEntry to carry over unchanged (same pointer)")
	}
	if len(p.LayoutLinksToAdd) != 1 || len(p.LayoutScriptsToAdd) != 1 {
		t.Errorf("unexpected layout link/script lists: %+v / %+v", p.LayoutLinksToAdd, p.LayoutScriptsToAdd)
	}
}

func TestProposalHasChanges(t *testing.T) {
	cases := []struct {
		name   string
		result *ai.Result
		want   bool
	}{
		{"empty", &ai.Result{}, false},
		{"files", &ai.Result{Files: []ai.GeneratedFile{{Path: "x"}}}, true},
		{"page entry", &ai.Result{PageRegistryEntry: &themefs.PageEntry{}}, true},
		{"layout links", &ai.Result{LayoutLinksToAdd: []string{"x"}}, true},
		{"layout scripts", &ai.Result{LayoutScriptsToAdd: []string{"x"}}, true},
	}
	for _, c := range cases {
		if got := proposalHasChanges(c.result); got != c.want {
			t.Errorf("%s: proposalHasChanges = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestClearIfNeedsClarification(t *testing.T) {
	result := &ai.Result{
		NeedsClarification: true,
		Files:              []ai.GeneratedFile{{Path: "x"}},
		PageRegistryEntry:  &themefs.PageEntry{},
		LayoutLinksToAdd:   []string{"x"},
		LayoutScriptsToAdd: []string{"x"},
	}
	clearIfNeedsClarification(result)
	if result.Files != nil || result.PageRegistryEntry != nil || result.LayoutLinksToAdd != nil || result.LayoutScriptsToAdd != nil {
		t.Errorf("expected all proposed changes cleared, got %+v", result)
	}
}

func TestClearIfNeedsClarification_NoOpWhenFalse(t *testing.T) {
	result := &ai.Result{Files: []ai.GeneratedFile{{Path: "x"}}}
	clearIfNeedsClarification(result)
	if len(result.Files) != 1 {
		t.Errorf("expected Files untouched when NeedsClarification is false, got %+v", result.Files)
	}
}

func TestSplitFindings(t *testing.T) {
	findings := []themecheck.Finding{
		{Rule: "a", Severity: themecheck.SeverityError},
		{Rule: "b", Severity: themecheck.SeverityWarning},
		{Rule: "c", Severity: themecheck.SeverityError},
	}
	errs, warns := splitFindings(findings)
	if len(errs) != 2 || len(warns) != 1 {
		t.Fatalf("expected 2 errors / 1 warning, got %d/%d", len(errs), len(warns))
	}
	if errs[0].Rule != "a" || errs[1].Rule != "c" || warns[0].Rule != "b" {
		t.Errorf("unexpected split: errs=%+v warns=%+v", errs, warns)
	}
}

func TestFindingRules(t *testing.T) {
	findings := []themecheck.Finding{{Rule: "a"}, {Rule: "b"}}
	got := findingRules(findings)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("unexpected rules: %+v", got)
	}
}

func TestSummarizeFindings(t *testing.T) {
	findings := []themecheck.Finding{
		{Rule: "known-fields", Path: "pages/offers.liquid", Message: "invented field"},
		{Rule: "theme-wide", Message: "no path here"},
	}
	got := summarizeFindings(findings)
	want := "[known-fields] pages/offers.liquid: invented field; [theme-wide] no path here"
	if got != want {
		t.Errorf("summarizeFindings = %q, want %q", got, want)
	}
}

func TestRecapAssistantTurn(t *testing.T) {
	result := &ai.Result{
		Summary: "Made an offers page.",
		Files:   []ai.GeneratedFile{{Path: "pages/offers.liquid", Action: "create", Content: "<p>hi</p>"}},
	}
	got := recapAssistantTurn(result)
	if got == "" {
		t.Fatal("expected non-empty recap")
	}
	if !strings.Contains(got, "Made an offers page.") || !strings.Contains(got, "pages/offers.liquid") || !strings.Contains(got, "<p>hi</p>") {
		t.Errorf("recap missing expected content: %q", got)
	}
}

func TestRecapAssistantTurn_NeverEmpty(t *testing.T) {
	got := recapAssistantTurn(&ai.Result{})
	if got == "" {
		t.Error("expected a non-empty fallback recap for a result with no summary or files")
	}
}

func TestRepairPrompt(t *testing.T) {
	findings := []themecheck.Finding{
		{Rule: "known-fields", Path: "pages/offers.liquid", Message: "invented field"},
	}
	got := repairPrompt(findings)
	if !strings.Contains(got, "known-fields") || !strings.Contains(got, "pages/offers.liquid") || !strings.Contains(got, "invented field") {
		t.Errorf("repair prompt missing expected content: %q", got)
	}
}

func TestAppendWarningsNote(t *testing.T) {
	summary := "Done."
	got := appendWarningsNote(summary, nil)
	if got != summary {
		t.Errorf("expected summary unchanged when there are no warnings, got %q", got)
	}

	warnings := []themecheck.Finding{{Rule: "seo-filled", Path: "pages/offers.liquid", Message: "seo_description is empty"}}
	got = appendWarningsNote(summary, warnings)
	if !strings.Contains(got, "Done.") || !strings.Contains(got, "1 warning") || !strings.Contains(got, "seo_description is empty") {
		t.Errorf("unexpected warnings note: %q", got)
	}
}

func TestFlattenFileTree(t *testing.T) {
	tree := []themefs.FileTreeEntry{
		{Name: "pages", Path: "pages", Type: "directory", Children: []themefs.FileTreeEntry{
			{Name: "offers.liquid", Path: "pages/offers.liquid", Type: "file"},
			{Name: "auth", Path: "pages/auth", Type: "directory", Children: []themefs.FileTreeEntry{
				{Name: "login.liquid", Path: "pages/auth/login.liquid", Type: "file"},
			}},
		}},
		{Name: "pages.json", Path: "pages.json", Type: "file"},
	}
	paths := make(map[string]bool)
	flattenFileTree(tree, paths)

	want := []string{"pages/offers.liquid", "pages/auth/login.liquid", "pages.json"}
	if len(paths) != len(want) {
		t.Fatalf("expected %d paths, got %d: %+v", len(want), len(paths), paths)
	}
	for _, p := range want {
		if !paths[p] {
			t.Errorf("expected path %q to be present", p)
		}
	}
	if paths["pages"] || paths["pages/auth"] {
		t.Errorf("expected directories not to be recorded as paths, got %+v", paths)
	}
}

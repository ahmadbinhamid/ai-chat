package themebuild

import (
	"strings"
	"testing"

	"ai-chat/internal/ai"
	"ai-chat/internal/themefs"
)

func TestValidateProposal_EditModeAllowsLiquidFiles(t *testing.T) {
	r := &ai.Result{Files: []ai.GeneratedFile{{Path: "pages/offers.liquid", Action: "update", Content: "hi"}}}
	if err := validateProposal(r, ""); err != nil {
		t.Errorf("expected no error in default (edit) mode, got: %v", err)
	}
}

func TestValidateProposal_EditModeAllowsDefaultsJSON(t *testing.T) {
	// defaults.json is a known, singular config file (see
	// themefs.allowedGeneratedFullPaths) — a brand/color/font request must
	// work in the default edit mode every chat actually runs in, not just
	// the brand mode nothing currently sets automatically (see
	// GenerateInput.Mode's doc comment).
	r := &ai.Result{Files: []ai.GeneratedFile{{Path: "defaults.json", Action: "update", Content: "{}"}}}
	if err := validateProposal(r, ""); err != nil {
		t.Errorf("expected defaults.json update to be allowed in edit mode, got: %v", err)
	}
}

func TestValidateProposal_BrandModeAllowsOnlyDefaultsJSON(t *testing.T) {
	r := &ai.Result{Files: []ai.GeneratedFile{{Path: "defaults.json", Action: "update", Content: "{}"}}}
	if err := validateProposal(r, ai.GenerationModeBrand); err != nil {
		t.Errorf("expected defaults.json update to be allowed in brand mode, got: %v", err)
	}
}

func TestValidateProposal_BrandModeRejectsOtherFiles(t *testing.T) {
	r := &ai.Result{Files: []ai.GeneratedFile{{Path: "pages/offers.liquid", Action: "update", Content: "hi"}}}
	if err := validateProposal(r, ai.GenerationModeBrand); err == nil {
		t.Error("expected a non-defaults.json file to be rejected in brand mode")
	}
}

func TestValidateProposal_BrandModeRejectsCreateAction(t *testing.T) {
	r := &ai.Result{Files: []ai.GeneratedFile{{Path: "defaults.json", Action: "create", Content: "{}"}}}
	if err := validateProposal(r, ai.GenerationModeBrand); err == nil {
		t.Error("expected a \"create\" action on defaults.json to be rejected (it always already exists)")
	}
}

func TestValidateProposal_BrandModeRejectsPageRegistration(t *testing.T) {
	r := &ai.Result{
		Files:             []ai.GeneratedFile{{Path: "defaults.json", Action: "update", Content: "{}"}},
		PageRegistryEntry: &themefs.PageEntry{Page: "offers"},
	}
	if err := validateProposal(r, ai.GenerationModeBrand); err == nil {
		t.Error("expected a page registration to be rejected in brand mode")
	}
}

// A files[] entry targeting layout-start.liquid (or layout-end.liquid)
// directly is allowed now — the AI theme builder can edit every real theme
// file, including these two (a deliberate decision; see
// pathsafety.go/writeplan.go's own doc comments for the history and the
// safety net that replaced the old outright rejection tested here before:
// buildWritePlan's hasDirectEdit guard, covered separately in
// service_test.go, not this function at all anymore).
func TestValidateProposal_EditModeAllowsDirectLayoutStartEdit(t *testing.T) {
	r := &ai.Result{
		Files:            []ai.GeneratedFile{{Path: pathLayoutStart, Action: "update", Content: "<html></html>"}},
		LayoutLinksToAdd: []string{"pages/css/offers.css"},
	}
	if err := validateProposal(r, ""); err != nil {
		t.Errorf("expected a files[] entry targeting layout-start.liquid to be allowed, got: %v", err)
	}
}

func TestValidateProposal_EditModeAllowsDirectLayoutEndEdit(t *testing.T) {
	r := &ai.Result{Files: []ai.GeneratedFile{{Path: pathLayoutEnd, Action: "update", Content: "</html>"}}}
	if err := validateProposal(r, ""); err != nil {
		t.Errorf("expected a files[] entry targeting layout-end.liquid to be allowed, got: %v", err)
	}
}

// The legitimate mechanism (layout_links_to_add/layout_scripts_to_add,
// with no files[] entry for the layout file itself) must still pass —
// otherwise the fix above would break the normal "register a new
// stylesheet" flow it's meant to leave alone.
func TestValidateProposal_EditModeAllowsLayoutLinksToAddWithoutDirectEdit(t *testing.T) {
	r := &ai.Result{
		Files:            []ai.GeneratedFile{{Path: "pages/offers.liquid", Action: "create", Content: "hi"}},
		LayoutLinksToAdd: []string{"pages/css/offers.css"},
	}
	if err := validateProposal(r, ""); err != nil {
		t.Errorf("expected layout_links_to_add without a direct files[] edit to layout-start.liquid to be allowed, got: %v", err)
	}
}

func TestValidateProposal_BrandModeRejectsLayoutRegistrations(t *testing.T) {
	r := &ai.Result{
		Files:            []ai.GeneratedFile{{Path: "defaults.json", Action: "update", Content: "{}"}},
		LayoutLinksToAdd: []string{"pages/css/offers.css"},
	}
	if err := validateProposal(r, ai.GenerationModeBrand); err == nil {
		t.Error("expected a layout link registration to be rejected in brand mode")
	}
}

func TestValidateProposal_RejectsEmptyContent(t *testing.T) {
	r := &ai.Result{Files: []ai.GeneratedFile{{Path: "components/store-hero-banner.liquid", Action: "update", Content: ""}}}
	err := validateProposal(r, "")
	if err == nil {
		t.Fatal("expected empty content to be rejected")
	}
	if !strings.Contains(err.Error(), "content must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateProposal_AllowsDelete(t *testing.T) {
	r := &ai.Result{Files: []ai.GeneratedFile{{Path: "pages/old-blog.liquid", Action: "delete", Content: ""}}}
	if err := validateProposal(r, ""); err != nil {
		t.Fatalf("delete should be allowed: %v", err)
	}
	r2 := &ai.Result{Files: []ai.GeneratedFile{{Path: "pages/home.liquid", Action: "delete", Content: ""}}}
	if err := validateProposal(r2, ""); err == nil {
		t.Fatal("deleting home.liquid must be rejected")
	}
}

func TestPendingFilesToPlan_SkipsEmptyContent(t *testing.T) {
	plan := pendingFilesToPlan([]GeneratedFile{
		{FilePath: "pages/home.liquid", Action: FileActionUpdate, Content: "HOME", Kind: GeneratedFileKindProposed},
		{FilePath: "components/store-hero-banner.liquid", Action: FileActionUpdate, Content: "", Kind: GeneratedFileKindProposed},
		{FilePath: pathLayoutStart, Action: FileActionUpdate, Content: "<html>", Kind: GeneratedFileKindLayout},
	})
	if len(plan.files) != 1 || plan.files[0].path != "pages/home.liquid" {
		t.Fatalf("expected only home.liquid, got %+v", plan.files)
	}
	if plan.layoutStart == nil {
		t.Fatal("expected layout-start to remain")
	}
}

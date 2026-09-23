package themebuild

import (
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
	// defaults.json must work in edit mode (brand mode is future).
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

// Direct layout edits now allowed; safety net guards against splice duplication.
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

// Layout links without direct edit must still work.
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

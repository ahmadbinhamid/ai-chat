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

func TestValidateProposal_BrandModeRejectsLayoutRegistrations(t *testing.T) {
	r := &ai.Result{
		Files:            []ai.GeneratedFile{{Path: "defaults.json", Action: "update", Content: "{}"}},
		LayoutLinksToAdd: []string{"pages/css/offers.css"},
	}
	if err := validateProposal(r, ai.GenerationModeBrand); err == nil {
		t.Error("expected a layout link registration to be rejected in brand mode")
	}
}

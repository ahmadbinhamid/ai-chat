package builderplan_test

import (
	"strings"
	"testing"

	"ai-chat/internal/buildercontract"
	"ai-chat/internal/builderplan"
)

func TestValidateRejectsNotImplementedOp(t *testing.T) {
	plan := builderplan.BuilderPlan{
		OriginalPrompt: "regenerate blog",
		Intent:         builderplan.IntentFullPage,
		Operations: []builderplan.Operation{{
			Kind:            builderplan.OperationKind(buildercontract.OpRegeneratePageContent),
			Label:           "Regenerate",
			ProtectExisting: true,
		}},
	}
	err := builderplan.Validate(plan)
	if err == nil {
		t.Fatal("expected reject of CONTRACT_DEFINED_NOT_IMPLEMENTED op")
	}
}

func TestValidateRejectsUnknownOp(t *testing.T) {
	plan := builderplan.BuilderPlan{
		OriginalPrompt: "do something",
		Intent:         builderplan.IntentSimpleEdit,
		Operations: []builderplan.Operation{{
			Kind:            "invented_lifecycle_op",
			ProtectExisting: true,
		}},
	}
	if err := builderplan.Validate(plan); err == nil {
		t.Fatal("expected reject of unknown op")
	}
}

func TestWithContractVersion(t *testing.T) {
	plan := builderplan.BuilderPlan{
		OriginalPrompt: "x",
		Intent:         builderplan.IntentAmbiguous,
		Operations:     []builderplan.Operation{{Kind: builderplan.OpClarify, ProtectExisting: true}},
	}
	out := builderplan.WithContractVersion(plan)
	found := false
	for _, c := range out.Constraints {
		if c == "contract_version="+builderplan.ContractVersion {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing contract_version constraint: %v", out.Constraints)
	}
	// idempotent
	out2 := builderplan.WithContractVersion(out)
	n := 0
	for _, c := range out2.Constraints {
		if strings.HasPrefix(c, "contract_version=") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected one contract_version, got %d in %v", n, out2.Constraints)
	}
}

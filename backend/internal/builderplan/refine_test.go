package builderplan_test

import (
	"testing"

	"ai-chat/internal/builderplan"
)

func TestApplyRefinement_RejectsUnsafe(t *testing.T) {
	t.Parallel()
	base, err := builderplan.BuildPlan("change button color to blue")
	if err != nil {
		t.Fatal(err)
	}
	_, err = builderplan.ApplyRefinement(base, builderplan.PlanRefinement{
		Intent: builderplan.IntentSimpleEdit,
		Operations: []builderplan.Operation{{
			Kind: "wipe_theme", Target: "*", ProtectExisting: true,
		}},
	})
	if err == nil {
		t.Fatal("expected rejection")
	}
	_, err = builderplan.ApplyRefinement(base, builderplan.PlanRefinement{
		Intent: builderplan.IntentPageCreate,
		Operations: []builderplan.Operation{{
			Kind: builderplan.OpCreatePage, Target: "../../etc/passwd", ProtectExisting: true,
		}},
	})
	if err == nil {
		t.Fatal("expected unsafe path rejection")
	}
}

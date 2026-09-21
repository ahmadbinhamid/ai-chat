package builderplan_test

import (
	"strings"
	"testing"

	"ai-chat/internal/builderplan"
)

func TestApplySemanticRefinement_MergesOnlySemantics(t *testing.T) {
	t.Parallel()
	base, err := builderplan.BuildPlan("change blogs for a software house but keep JPRO meta titles")
	if err != nil {
		t.Fatal(err)
	}
	intent, ops := base.Intent, len(base.Operations)
	out, err := builderplan.ApplySemanticRefinement(base,
		[]string{"adapt blog content for software house audience"},
		[]string{"meta_title"},
		[]string{"software house"},
		"", false, 0.91, "test",
	)
	if err != nil {
		t.Fatal(err)
	}
	if out.Intent != intent || len(out.Operations) != ops {
		t.Fatal("core plan must stay")
	}
	blob := strings.Join(out.Constraints, " ")
	if !strings.Contains(blob, "protect_field:meta_title") {
		t.Fatalf("constraints=%v", out.Constraints)
	}
	if !strings.Contains(blob, "preference:software house") {
		t.Fatalf("constraints=%v", out.Constraints)
	}
}

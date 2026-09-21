package buildershadow

import (
	"context"
	"testing"
	"time"

	"ai-chat/internal/builderintelligence"
	"ai-chat/internal/builderplan"
)

func TestRunner_DisabledNoOp(t *testing.T) {
	r := New(Config{Enabled: false})
	cmp := r.Observe(context.Background(), Input{
		Prompt:            "change button color",
		DeterministicPlan: mustPlan(t, "change button color to blue"),
	})
	if !cmp.Skipped || cmp.SkipReason != "disabled" {
		t.Fatalf("%+v", cmp)
	}
}

func TestRunner_NoCandidateConfigured(t *testing.T) {
	r := New(Config{Enabled: true, Provider: "unavailable", Blocking: true})
	cmp := r.Observe(context.Background(), Input{
		Prompt:            "x",
		DeterministicPlan: mustPlan(t, "change button color to blue"),
	})
	if !cmp.Skipped || cmp.SkipReason != "no_candidate_model_configured" {
		t.Fatalf("%+v", cmp)
	}
}

func TestRunner_ShadowDoesNotMutateProductionPlan(t *testing.T) {
	r := New(Config{
		Enabled:  true,
		Provider: "heuristic",
		Blocking: true,
		Timeout:  time.Second,
	})
	base := mustPlan(t, "change the blogs according to software house but keep JPRO meta titles")
	prod := base
	prod.Constraints = append(append([]string{}, base.Constraints...), "production_only_marker")

	before := append([]string{}, prod.Constraints...)
	cmp := r.Observe(context.Background(), Input{
		Prompt:            base.OriginalPrompt,
		DeterministicPlan: base,
		ProductionPlan:    prod,
		ProductionMeta:    builderintelligence.Meta{Provider: "production_off", RefinementApplied: false},
	})
	if cmp.Skipped {
		t.Fatalf("expected shadow run: %+v", cmp)
	}
	if cmp.Status != StatusDiscarded {
		t.Fatalf("status=%s", cmp.Status)
	}
	if len(prod.Constraints) != len(before) {
		t.Fatal("production plan mutated")
	}
	for i := range before {
		if prod.Constraints[i] != before[i] {
			t.Fatal("production plan mutated")
		}
	}
	if !cmp.CandidateValid {
		t.Fatalf("heuristic candidate should validate: %+v", cmp)
	}
	if cmp.CandidateUnsafe {
		t.Fatalf("unsafe: %+v", cmp)
	}
}

func TestCompare_ExactEmpty(t *testing.T) {
	p := mustPlan(t, "change button color to blue")
	c := Compare(p, p, builderintelligence.Meta{}, builderintelligence.Meta{}, true, false, 1)
	if !c.ExactMatch || c.ConstraintsF1 != 1 {
		t.Fatalf("%+v", c)
	}
}

func mustPlan(t *testing.T, prompt string) builderplan.BuilderPlan {
	t.Helper()
	p, err := builderplan.BuildPlan(prompt)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

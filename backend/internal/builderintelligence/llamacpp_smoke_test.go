//go:build llamasmoke

package builderintelligence_test

import (
	"context"
	"os"
	"testing"
	"time"

	"ai-chat/internal/builderintelligence"
	"ai-chat/internal/builderplan"
)

func TestLlamaCPP_SemanticSmoke(t *testing.T) {
	url := os.Getenv("BUILDER_LOCAL_LM_URL")
	if url == "" {
		url = "http://127.0.0.1:8090/v1"
	}
	svc := builderintelligence.New(builderintelligence.Config{
		Enabled: true, Provider: builderintelligence.ProviderLlamaCPP,
		URL: url, Model: "qwen2.5-0.5b-instruct", Timeout: 1500 * time.Millisecond,
	})
	cases := []struct {
		prompt   string
		wantCall bool
	}{
		{"create 2 blog pages", false},
		{"can you register the blog page", false},
		{"change button color to blue", false},
		{"change the blogs according to software house but keep JPRO meta titles", true},
		{"make the blog more professional for SaaS customers and don't change its slug", true},
		{"make the site better", true},
		{"change the blog title but leave its meta description and slug alone", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.prompt, func(t *testing.T) {
			plan, err := builderplan.BuildPlan(tc.prompt)
			if err != nil {
				t.Fatal(err)
			}
			res := svc.Understand(context.Background(), builderintelligence.Input{
				Prompt: tc.prompt, DeterministicPlan: plan,
			})
			t.Logf("called=%v skipped=%v reason=%s success=%v fail=%v timeout=%v fallback=%v in=%d out=%d elapsed_ms=%d fields=%d applied=%v intent=%s→%s ops=%d→%d constraints=%v",
				res.Meta.Called, res.Meta.Skipped, res.Meta.Reason, res.Meta.Success, res.Meta.Failure,
				res.Meta.Timeout, res.Meta.Fallback, res.Meta.InputBytes, res.Meta.OutputBytes, res.Meta.ElapsedMs,
				res.Meta.RefinementFieldsCount, res.Meta.RefinementApplied,
				res.Meta.PlanIntentBefore, res.Meta.PlanIntentAfter,
				res.Meta.PlanOpCountBefore, res.Meta.PlanOpCountAfter, res.Plan.Constraints)
			if tc.wantCall && res.Meta.Skipped && res.Meta.Reason != "disabled" {
				// may still skip if policy disagrees — fail hard
				if !builderintelligence.ShouldCall(plan).Call {
					t.Fatalf("policy says skip but test expected call")
				}
			}
			if !tc.wantCall && res.Meta.Called {
				t.Fatalf("expected skip, got call")
			}
			if res.Plan.Intent != plan.Intent {
				t.Fatalf("intent must stay deterministic")
			}
			if len(res.Plan.Operations) != len(plan.Operations) {
				t.Fatalf("ops must stay deterministic")
			}
			if err := builderplan.Validate(res.Plan); err != nil {
				t.Fatal(err)
			}
		})
	}
}

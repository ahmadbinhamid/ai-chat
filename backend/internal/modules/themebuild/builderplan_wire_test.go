package themebuild

import (
	"strings"
	"testing"
	"time"

	"ai-chat/internal/builderplan"
)

func TestObserveBuilderPlan_RealCases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		prompt         string
		wantIntent     builderplan.IntentKind
		wantCompound   bool
		wantMinOps     int
		wantOp         builderplan.OperationKind
		wantComplexity builderplan.Complexity
		wantConstraint string
		wantCtxAnyOf   []string
		wantDeepSeek   bool
	}{
		{
			name: "create_2_blog_pages", prompt: "create 2 blog pages",
			wantIntent: builderplan.IntentCompound, wantCompound: true, wantMinOps: 2,
			wantComplexity: builderplan.ComplexityHigh, wantDeepSeek: true,
			wantOp: builderplan.OpCreatePage,
		},
		{
			name: "change_blog_meta_titles", prompt: "change the blog meta titles",
			wantIntent: builderplan.IntentSEOMeta, wantMinOps: 1,
			wantOp: builderplan.OpUpdateSEOMeta, wantDeepSeek: true,
		},
		{
			name: "blog_content_software_house", prompt: "change blog content according to a software house",
			wantIntent: builderplan.IntentFullPage, wantMinOps: 1,
			wantOp: builderplan.OpUpdatePageContent, wantCtxAnyOf: []string{"pages/blog.liquid"},
			wantDeepSeek: true,
		},
		{
			name: "blogs_and_only_meta_titles", prompt: "change the blogs and update only meta titles",
			wantIntent: builderplan.IntentCompound, wantCompound: true, wantMinOps: 2,
			wantConstraint: "seo_titles_only_preserve_other_seo_fields", wantDeepSeek: true,
		},
		{
			name: "button_color_blue", prompt: "change button color to blue",
			wantIntent: builderplan.IntentSimpleEdit, wantMinOps: 1,
			wantOp: builderplan.OpSimpleStyleEdit, wantDeepSeek: true,
			wantComplexity: builderplan.ComplexityLow,
		},
		{
			name: "register_blog_if_missing", prompt: "if the blog page is not registered, register it",
			wantIntent: builderplan.IntentNavigationRegistry, wantMinOps: 1,
			wantOp: builderplan.OpRegisterPage, wantDeepSeek: true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			start := time.Now()
			obs := observeBuilderPlan(tc.prompt)
			elapsed := time.Since(start)
			if !obs.Valid {
				t.Fatalf("invalid plan: %s", obs.FallbackReason)
			}
			if obs.Plan.Intent != tc.wantIntent {
				t.Fatalf("intent=%s want %s ops=%v", obs.Plan.Intent, tc.wantIntent, obs.Plan.Operations)
			}
			if obs.Plan.Compound != tc.wantCompound {
				t.Fatalf("compound=%v want %v", obs.Plan.Compound, tc.wantCompound)
			}
			if len(obs.Plan.Operations) < tc.wantMinOps {
				t.Fatalf("ops=%d want>=%d (%v)", len(obs.Plan.Operations), tc.wantMinOps, obs.Plan.Operations)
			}
			if tc.wantComplexity != "" && obs.Plan.Complexity == builderplan.ComplexityLow && tc.wantComplexity != builderplan.ComplexityLow {
				t.Fatalf("complexity=%s want not simple/low for compound-like", obs.Plan.Complexity)
			}
			if tc.wantComplexity == builderplan.ComplexityHigh && obs.Plan.Complexity == builderplan.ComplexityLow {
				t.Fatalf("complexity too low: %s", obs.Plan.Complexity)
			}
			if tc.wantOp != "" {
				found := false
				for _, op := range obs.Plan.Operations {
					if op.Kind == tc.wantOp {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("missing op %s in %v", tc.wantOp, obs.Plan.Operations)
				}
			}
			if tc.wantConstraint != "" {
				found := false
				for _, c := range obs.Plan.Constraints {
					if c == tc.wantConstraint {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("missing constraint %q in %v", tc.wantConstraint, obs.Plan.Constraints)
				}
			}
			if len(tc.wantCtxAnyOf) > 0 {
				found := false
				for _, want := range tc.wantCtxAnyOf {
					for _, got := range obs.CandidateContext {
						if got == want {
							found = true
						}
					}
				}
				if !found {
					t.Fatalf("context %v missing any of %v", obs.CandidateContext, tc.wantCtxAnyOf)
				}
			}
			if obs.NeedsDeepSeek != tc.wantDeepSeek {
				t.Fatalf("NeedsDeepSeek=%v want %v", obs.NeedsDeepSeek, tc.wantDeepSeek)
			}
			// Planner is CPU-only; expect sub-second (typically ms). No hard SLA claim.
			if elapsed > time.Second {
				t.Fatalf("planner too slow: %v", elapsed)
			}
			t.Logf("planner_ms=%d context_ms=%d total=%v intent=%s ops=%d ctx=%d",
				obs.PlannerElapsedMs, obs.ContextElapsedMs, elapsed,
				obs.Plan.Intent, len(obs.Plan.Operations), len(obs.CandidateContext))
		})
	}
}

func TestEscalateIntentFromPlan_NeverDowngrades(t *testing.T) {
	t.Parallel()
	obs := observeBuilderPlan("change button color to blue")
	got, applied := escalateIntentFromPlan(IntentComplexPage, obs)
	if applied || got != IntentComplexPage {
		t.Fatalf("must not downgrade complex → simple: got=%s applied=%v", got, applied)
	}
}

func TestEscalateIntentFromPlan_EscalatesSimpleWhenCompound(t *testing.T) {
	t.Parallel()
	obs := observeBuilderPlan("create 2 blog pages")
	got, applied := escalateIntentFromPlan(IntentSimpleEdit, obs)
	if !applied || got != IntentComplexPage {
		t.Fatalf("got=%s applied=%v want complex escalate", got, applied)
	}
}

func TestNarrowPathsWithCandidates_SafeSubsetOnly(t *testing.T) {
	t.Parallel()
	existing := []string{"a.liquid", "b.liquid", "c.liquid"}
	got, ok := narrowPathsWithCandidates(existing, []string{"a.liquid", "b.liquid"})
	if !ok || len(got) != 2 {
		t.Fatalf("got=%v ok=%v", got, ok)
	}
	got, ok = narrowPathsWithCandidates(existing, []string{"a.liquid", "missing.liquid"})
	if ok {
		t.Fatalf("must not narrow with missing candidate: %v", got)
	}
}

func TestMapBuilderPlanToExistingIntent(t *testing.T) {
	t.Parallel()
	plan, err := builderplan.BuildPlan("change button color to blue")
	if err != nil {
		t.Fatal(err)
	}
	intent, mode, ok := mapBuilderPlanToExistingIntent(plan)
	if !ok || intent != IntentSimpleEdit || mode != "simple_edit" {
		t.Fatalf("intent=%s mode=%s ok=%v", intent, mode, ok)
	}
	plan, err = builderplan.BuildPlan("create 2 blog pages")
	if err != nil {
		t.Fatal(err)
	}
	intent, mode, ok = mapBuilderPlanToExistingIntent(plan)
	if !ok || intent != IntentComplexPage || !strings.Contains(mode, "complex") {
		t.Fatalf("intent=%s mode=%s ok=%v", intent, mode, ok)
	}
}

func TestBuilderPlanDisabledByDefault(t *testing.T) {
	t.Parallel()
	s := &Service{}
	if s.builderPlanEnabled {
		t.Fatal("BuilderPlan must default OFF")
	}
	s.SetBuilderPlanEnabled(true)
	if !s.builderPlanEnabled {
		t.Fatal("SetBuilderPlanEnabled failed")
	}
}

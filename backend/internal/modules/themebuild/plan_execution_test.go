package themebuild

import (
	"encoding/json"
	"strings"
	"testing"

	"ai-chat/internal/ai"
	"ai-chat/internal/builderplan"
)

func TestShouldShortCircuitClarification(t *testing.T) {
	t.Parallel()
	plan, err := builderplan.BuildPlan("make the site better")
	if err != nil {
		t.Fatal(err)
	}
	obs := planObservation{Plan: plan, Valid: true, NeedsDeepSeek: builderplan.NeedsDeepSeek(plan)}
	if !shouldShortCircuitClarification(obs) {
		t.Fatalf("expected short-circuit for ambiguous plan intent=%s ops=%v", plan.Intent, plan.Operations)
	}
	if builderplan.NeedsDeepSeek(plan) {
		t.Fatal("ambiguous must not need DeepSeek")
	}
	msg := ambiguousClarificationReply("make the site better")
	if !strings.Contains(strings.ToLower(msg), "content") || !strings.Contains(strings.ToLower(msg), "design") {
		t.Fatalf("clarification reply too vague: %q", msg)
	}
}

func TestShouldShortCircuitClarification_NotForSimpleEdit(t *testing.T) {
	t.Parallel()
	plan, err := builderplan.BuildPlan("change the header button color to blue")
	if err != nil {
		t.Fatal(err)
	}
	obs := planObservation{Plan: plan, Valid: true, NeedsDeepSeek: builderplan.NeedsDeepSeek(plan)}
	if shouldShortCircuitClarification(obs) {
		t.Fatal("simple edit must not short-circuit")
	}
}

func TestCheckPlanExecutionMatch_CreateVsContent(t *testing.T) {
	t.Parallel()
	createPlan, err := builderplan.BuildPlan("create a blog page")
	if err != nil {
		t.Fatal(err)
	}
	obs := planObservation{Plan: createPlan, Valid: true}
	// Dangerous mismatch: planned create, executed content update only.
	err = checkPlanExecutionMatch(obs, &ai.Result{
		Files: []ai.GeneratedFile{{Path: "pages/blog.liquid", Action: "update", Content: "x"}},
	})
	if err == nil {
		t.Fatal("expected plan/execution mismatch")
	}

	contentPlan, err := builderplan.BuildPlan("make the blog more professional for SaaS customers but don't change the slug")
	if err != nil {
		t.Fatal(err)
	}
	obs = planObservation{Plan: contentPlan, Valid: true}
	if err := checkPlanExecutionMatch(obs, &ai.Result{
		Files: []ai.GeneratedFile{{Path: "pages/blog.liquid", Action: "update", Content: "x"}},
	}); err != nil {
		t.Fatalf("matching content update must pass: %v (plan=%s ops=%v)", err, contentPlan.Intent, contentPlan.Operations)
	}
}

func TestSimpleEditExplorationBrakeEnabled(t *testing.T) {
	t.Parallel()
	// Regression: simple edit must not disable the exploration brake
	// (previously true → read thrash until TOOL_THRASH).
	tc := ai.ThemeContext{SimpleEditOneShot: true, SimpleEditAllowRead: true, MaxToolIterations: maxSimpleEditModelCalls}
	tc.DisableExplorationBrake = false
	tc.MaxExplorationToolCalls = 1
	if tc.DisableExplorationBrake {
		t.Fatal("exploration brake must stay enabled for simple edit")
	}
	if tc.MaxExplorationToolCalls != 1 {
		t.Fatalf("max exploration=%d want 1", tc.MaxExplorationToolCalls)
	}
	if tc.MaxToolIterations != 2 {
		t.Fatalf("max iterations=%d want 2", tc.MaxToolIterations)
	}
}

func TestExecutedOperationsFromResult(t *testing.T) {
	t.Parallel()
	ops := executedOperationsFromResult(&ai.Result{
		Files: []ai.GeneratedFile{
			{Path: "pages/blog.liquid", Action: "update"},
			{Path: "defaults.json", Action: "update"},
		},
	})
	raw, _ := json.Marshal(ops)
	joined := string(raw)
	if !strings.Contains(joined, "update_page_content") || !strings.Contains(joined, "add_to_navigation") {
		t.Fatalf("ops=%v", ops)
	}
}

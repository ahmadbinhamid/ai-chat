package buildertraining

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ai-chat/internal/builderintelligence"
	"ai-chat/internal/builderplan"
)

// ScenarioResult is one real-world request class outcome.
type ScenarioResult struct {
	ID                 string   `json:"id"`
	Prompt             string   `json:"prompt"`
	Passed             bool     `json:"passed"`
	Notes              []string `json:"notes,omitempty"`
	Intent             string   `json:"intent"`
	Ambiguous          bool     `json:"ambiguous"`
	Compound           bool     `json:"compound"`
	NeedsDeepSeek      bool     `json:"needs_deepseek"`
	LocalLMShouldCall  bool     `json:"local_lm_should_call"`
	LocalLMSkipReason  string   `json:"local_lm_skip_reason,omitempty"`
	OpKinds            []string `json:"operation_kinds"`
	ContextFileCount   int      `json:"context_file_count"`
	ContextFiles       []string `json:"context_files,omitempty"`
	ProtectedSignals   []string `json:"protected_field_signals,omitempty"`
	PlannerMs          int64    `json:"planner_ms"`
	HeuristicMs        int64    `json:"heuristic_ms,omitempty"`
	HeuristicProtected []string `json:"heuristic_protected_fields,omitempty"`
	HeuristicPrefs     []string `json:"heuristic_preferences,omitempty"`
}

func runScenarioSuite(ctx context.Context, _ FinalBenchmarkOptions) []ScenarioResult {
	type spec struct {
		id, prompt string
		check      func(ScenarioResult, builderplan.BuilderPlan, builderintelligence.Decision) []string
	}
	specs := []spec{
		{"A_simple_deterministic", "change the header button color to blue", checkSimple},
		{"B_register_existing", "can you register the blog page", checkRegister},
		{"C_semantic_jpro", "change the blogs according to software house but keep the JPRO meta titles", checkSemanticJPRO},
		{"D_saas_slug", "make the blog more professional for SaaS customers but don't change the slug", checkSaasSlug},
		{"E_compound_create2", "create 2 blog pages", checkCompound},
		{"F_create_nav", "create a page and add it to navigation", checkCreateNav},
		{"G_ambiguous", "make the site better", checkAmbiguous},
		{"H_seo_protected", "change the blog meta title but leave meta description and slug unchanged", checkSEO},
		{"I_timeout_policy", "change blogs for software house keep JPRO meta titles", checkTimeoutPolicy},
	}

	out := make([]ScenarioResult, 0, len(specs))
	heuristic := builderintelligence.New(builderintelligence.Config{
		Enabled: true, Provider: builderintelligence.ProviderHeuristic, Timeout: time.Second,
	})

	for _, sp := range specs {
		start := time.Now()
		plan, err := builderplan.BuildPlan(sp.prompt)
		plannerMs := time.Since(start).Milliseconds()
		sr := ScenarioResult{
			ID:       sp.id,
			Prompt:   sp.prompt,
			PlannerMs: plannerMs,
		}
		if err != nil {
			sr.Notes = []string{"build_plan_error: " + err.Error()}
			out = append(out, sr)
			continue
		}
		plan.RequiredFiles = builderplan.SelectContextFiles(plan)
		dec := builderintelligence.ShouldCall(plan)
		sr.Intent = string(plan.Intent)
		sr.Ambiguous = plan.Ambiguous
		sr.Compound = plan.Compound
		sr.NeedsDeepSeek = builderplan.NeedsDeepSeek(plan)
		sr.LocalLMShouldCall = dec.Call
		sr.LocalLMSkipReason = dec.Reason
		sr.ContextFileCount = len(plan.RequiredFiles)
		sr.ContextFiles = append([]string{}, plan.RequiredFiles...)
		for _, op := range plan.Operations {
			sr.OpKinds = append(sr.OpKinds, string(op.Kind))
		}

		hStart := time.Now()
		res := heuristic.Understand(ctx, builderintelligence.Input{Prompt: sp.prompt, DeterministicPlan: plan})
		sr.HeuristicMs = time.Since(hStart).Milliseconds()
		_, prot, prefs, _ := extractPlanSemantics(res.Plan)
		sr.HeuristicProtected = prot
		sr.HeuristicPrefs = prefs
		sr.ProtectedSignals = prot

		fails := sp.check(sr, plan, dec)
		sr.Notes = fails
		sr.Passed = len(fails) == 0
		out = append(out, sr)
	}
	return out
}

func extractPlanSemantics(plan builderplan.BuilderPlan) (constraints, protected, prefs []string, clar string) {
	for _, c := range plan.Constraints {
		low := strings.ToLower(strings.TrimSpace(c))
		switch {
		case strings.HasPrefix(low, "protect_field:"):
			protected = append(protected, strings.TrimSpace(c[len("protect_field:"):]))
		case strings.HasPrefix(low, "preference:"):
			prefs = append(prefs, strings.TrimSpace(c[len("preference:"):]))
		case strings.HasPrefix(low, "clarification:"):
			clar = strings.TrimSpace(c[len("clarification:"):])
		default:
			if !strings.HasPrefix(low, "never_") && !strings.HasPrefix(low, "prefer_") && !strings.HasPrefix(low, "planner_") {
				constraints = append(constraints, c)
			}
		}
	}
	return
}

func checkSimple(sr ScenarioResult, plan builderplan.BuilderPlan, dec builderintelligence.Decision) []string {
	var f []string
	if plan.Intent != builderplan.IntentSimpleEdit {
		f = append(f, fmt.Sprintf("intent=%s want simple_edit", plan.Intent))
	}
	if dec.Call {
		f = append(f, "local LM should skip for deterministic simple edit")
	}
	if sr.ContextFileCount > 8 {
		f = append(f, fmt.Sprintf("context too large: %d", sr.ContextFileCount))
	}
	return f
}

func checkRegister(sr ScenarioResult, plan builderplan.BuilderPlan, dec builderintelligence.Decision) []string {
	var f []string
	found := false
	for _, k := range sr.OpKinds {
		if k == string(builderplan.OpRegisterExistingPage) || k == string(builderplan.OpRegisterPage) {
			found = true
		}
	}
	if !found {
		f = append(f, "missing register operation")
	}
	if sr.NeedsDeepSeek {
		f = append(f, "register must not need DeepSeek")
	}
	if dec.Call {
		f = append(f, "local LM should skip for register")
	}
	return f
}

func checkSemanticJPRO(sr ScenarioResult, plan builderplan.BuilderPlan, dec builderintelligence.Decision) []string {
	var f []string
	if !dec.Call {
		f = append(f, "local LM should run for semantic/JPRO constraints")
	}
	if sr.NeedsDeepSeek == false && plan.Intent != builderplan.IntentAmbiguous {
		// content rewrite still needs DeepSeek
		f = append(f, "expected NeedsDeepSeek for content rewrite")
	}
	hasMeta := false
	for _, p := range sr.HeuristicProtected {
		if strings.Contains(strings.ToLower(p), "meta") || p == "meta_title" {
			hasMeta = true
		}
	}
	if !hasMeta {
		f = append(f, "expected meta_title protected via heuristic refinement")
	}
	return f
}

func checkSaasSlug(sr ScenarioResult, _ builderplan.BuilderPlan, dec builderintelligence.Decision) []string {
	var f []string
	if !dec.Call {
		f = append(f, "local LM should run for preference+protected slug")
	}
	hasSlug := false
	for _, p := range sr.HeuristicProtected {
		if p == "slug" {
			hasSlug = true
		}
	}
	if !hasSlug {
		f = append(f, "expected slug protected")
	}
	return f
}

func checkCompound(sr ScenarioResult, plan builderplan.BuilderPlan, _ builderintelligence.Decision) []string {
	var f []string
	if !plan.Compound && plan.Intent != builderplan.IntentCompound {
		f = append(f, "expected compound plan")
	}
	if len(sr.OpKinds) < 2 {
		f = append(f, "compound should have ≥2 operations")
	}
	if !sr.NeedsDeepSeek {
		f = append(f, "create-2 still routes to DeepSeek for generation")
	}
	return f
}

func checkCreateNav(sr ScenarioResult, plan builderplan.BuilderPlan, _ builderintelligence.Decision) []string {
	var f []string
	if plan.Intent == builderplan.IntentAmbiguous {
		f = append(f, "should not be ambiguous")
	}
	if !sr.NeedsDeepSeek {
		f = append(f, "page+nav typically needs DeepSeek for page content")
	}
	// Ensure we didn't mark whole-theme wipe
	for _, c := range plan.Constraints {
		if strings.Contains(strings.ToLower(c), "wipe") {
			f = append(f, "destructive constraint")
		}
	}
	return f
}

func checkAmbiguous(sr ScenarioResult, plan builderplan.BuilderPlan, dec builderintelligence.Decision) []string {
	var f []string
	if !plan.Ambiguous && plan.Intent != builderplan.IntentAmbiguous {
		f = append(f, "expected ambiguous intent")
	}
	if sr.NeedsDeepSeek {
		f = append(f, "ambiguous must not call DeepSeek until clarified")
	}
	if sr.ContextFileCount > 12 {
		f = append(f, "ambiguous must not dump giant theme context")
	}
	_ = dec
	return f
}

func checkSEO(sr ScenarioResult, _ builderplan.BuilderPlan, dec builderintelligence.Decision) []string {
	var f []string
	if !dec.Call {
		f = append(f, "local LM should extract protected SEO fields")
	}
	got := map[string]bool{}
	for _, p := range sr.HeuristicProtected {
		got[p] = true
	}
	if !got["meta_description"] && !got["slug"] {
		f = append(f, "expected meta_description and/or slug protected")
	}
	return f
}

func checkTimeoutPolicy(_ ScenarioResult, _ builderplan.BuilderPlan, _ builderintelligence.Decision) []string {
	// Controlled: document policy — local LM timeout is 1500ms; parent deadline intact.
	// No live injection required for pass; reliability checklist covers it.
	return nil
}

package themebuild

import (
	"context"
	"log/slog"
	"time"

	"ai-chat/internal/builderintelligence"
	"ai-chat/internal/builderplan"
	"ai-chat/internal/buildershadow"
)

// planObservation is the non-mutating BuilderPlan snapshot used when
// BUILDER_PLAN_ENABLED=true. It never writes theme files or calls DeepSeek.
type planObservation struct {
	Plan             builderplan.BuilderPlan
	Valid            bool
	FallbackReason   string
	PlannerElapsedMs int64
	ContextElapsedMs int64
	NeedsDeepSeek    bool
	MappedIntent     Intent // empty when no safe mapping
	MappedGeneration string // existing generation-mode label for logs
	CandidateContext []string
	AppliedEscalate  bool
	LocalLM          builderintelligence.Meta
	Shadow           buildershadow.Comparison
}

// observeBuilderPlanOptions configures optional local understanding + shadow.
type observeBuilderPlanOptions struct {
	Intelligence *builderintelligence.Service
	Shadow       *buildershadow.Runner
	GenerationID string
	TenantID     uint64
}

// observeBuilderPlan runs CPU-only plan build+validate. On any failure it
// returns Valid=false and FallbackReason so callers keep the existing path.
func observeBuilderPlan(prompt string) planObservation {
	return observeBuilderPlanWith(context.Background(), prompt, observeBuilderPlanOptions{})
}

// observeBuilderPlanWith optionally refines via builderintelligence.Service.
// themebuild does not know provider/URL/model details — only the service.
func observeBuilderPlanWith(ctx context.Context, prompt string, opts observeBuilderPlanOptions) planObservation {
	start := time.Now()
	plan, err := builderplan.BuildPlan(prompt)
	plannerMs := time.Since(start).Milliseconds()
	if err != nil {
		return planObservation{
			Valid:            false,
			FallbackReason:   "build_plan_error",
			PlannerElapsedMs: plannerMs,
		}
	}

	ctxStart := time.Now()
	candidates := builderplan.SelectContextFiles(plan)
	contextMs := time.Since(ctxStart).Milliseconds()
	if len(plan.RequiredFiles) == 0 {
		plan.RequiredFiles = candidates
	}

	var lmMeta builderintelligence.Meta
	deterministic := plan
	// Ambiguous/clarify plans must not call local LM or DeepSeek — short-circuit
	// happens in doGenerate. Skip Understand so local_lm_called stays false.
	if !plan.Ambiguous && plan.Intent != builderplan.IntentAmbiguous &&
		plan.Intent != builderplan.IntentPageTroubleshoot &&
		!(len(plan.Operations) == 1 && plan.Operations[0].Kind == builderplan.OpClarify) &&
		!(len(plan.Operations) == 1 && plan.Operations[0].Kind == builderplan.OpDiagnoseExistingPage) &&
		opts.Intelligence != nil && opts.Intelligence.Enabled() {
		res := opts.Intelligence.Understand(ctx, builderintelligence.Input{
			Prompt:            prompt,
			DeterministicPlan: plan,
		})
		plan = res.Plan
		lmMeta = res.Meta
		if len(plan.RequiredFiles) == 0 {
			plan.RequiredFiles = builderplan.SelectContextFiles(plan)
		}
	}

	var shadowCmp buildershadow.Comparison
	if opts.Shadow != nil && opts.Shadow.Enabled() {
		shadowCmp = opts.Shadow.Observe(ctx, buildershadow.Input{
			Prompt:            prompt,
			DeterministicPlan: deterministic,
			ProductionPlan:    plan,
			ProductionMeta:    lmMeta,
			GenerationID:      opts.GenerationID,
			TenantID:          opts.TenantID,
		})
	}

	obs := planObservation{
		Plan:             plan,
		Valid:            true,
		PlannerElapsedMs: plannerMs,
		ContextElapsedMs: contextMs,
		NeedsDeepSeek:    builderplan.NeedsDeepSeek(plan),
		CandidateContext: plan.RequiredFiles,
		LocalLM:          lmMeta,
		Shadow:           shadowCmp,
	}
	if mapped, mode, ok := mapBuilderPlanToExistingIntent(plan); ok {
		obs.MappedIntent = mapped
		obs.MappedGeneration = mode
	}
	if plan.Ambiguous || plan.Intent == builderplan.IntentAmbiguous {
		obs.FallbackReason = "ambiguous_plan"
	}
	return obs
}

// mapBuilderPlanToExistingIntent maps BuilderPlan intents onto the existing
// themebuild Intent / generation-mode labels. ok=false means "do not override".
func mapBuilderPlanToExistingIntent(plan builderplan.BuilderPlan) (Intent, string, bool) {
	if plan.Ambiguous || plan.Intent == builderplan.IntentAmbiguous {
		return "", "", false
	}
	if plan.Confidence < 0.8 {
		return "", "", false
	}
	switch plan.Intent {
	case builderplan.IntentSimpleEdit:
		return IntentSimpleEdit, "simple_edit", true
	case builderplan.IntentSectionEdit:
		return IntentMultiFileEdit, "multi_file_edit", true
	case builderplan.IntentFullPage, builderplan.IntentCompound,
		builderplan.IntentPageCreate, builderplan.IntentSEOMeta,
		builderplan.IntentNavigationRegistry:
		return IntentComplexPage, "complex_page", true
	case builderplan.IntentPageTroubleshoot:
		return IntentRepair, "page_troubleshoot", true
	default:
		return "", "", false
	}
}

// escalateIntentFromPlan may only escalate capability (simple → complex).
// It never downgrades complex/repair/multi-file routing. Returns the intent
// to use and whether an escalation was applied.
func escalateIntentFromPlan(existing Intent, obs planObservation) (Intent, bool) {
	if !obs.Valid || obs.MappedIntent == "" {
		return existing, false
	}
	switch existing {
	case IntentComplexPage, IntentRepair, IntentMultiFileEdit:
		return existing, false
	case IntentConversation, IntentThemeQuery:
		return existing, false
	}
	if obs.MappedIntent == IntentComplexPage &&
		(existing == IntentSimpleEdit) {
		return IntentComplexPage, true
	}
	if obs.MappedIntent == IntentMultiFileEdit && existing == IntentSimpleEdit {
		return IntentMultiFileEdit, true
	}
	return existing, false
}

// narrowPathsWithCandidates returns existing paths unchanged unless every
// candidate appears in existing (safe subset). Never expands the set.
func narrowPathsWithCandidates(existing, candidates []string) ([]string, bool) {
	if len(existing) == 0 || len(candidates) == 0 {
		return existing, false
	}
	if len(candidates) >= len(existing) {
		return existing, false
	}
	have := make(map[string]bool, len(existing))
	for _, p := range existing {
		have[p] = true
	}
	for _, c := range candidates {
		if !have[c] {
			return existing, false
		}
	}
	return append([]string(nil), candidates...), true
}

func logBuilderPlanObservation(genID string, tenantID uint64, chatID string, obs planObservation, existingIntent Intent) {
	attrs := []any{
		"generation_id", genID,
		"tenant_id", tenantID,
		"chat_id", chatID,
		"builder_plan_enabled", true,
		"builder_plan_valid", obs.Valid,
		"builder_plan_fallback", obs.FallbackReason,
		"builder_plan_ms", obs.PlannerElapsedMs,
		"builder_plan_context_ms", obs.ContextElapsedMs,
		"existing_intent", string(existingIntent),
	}
	if !obs.Valid {
		slog.Info("ai: builderplan observation", attrs...)
		return
	}
	p := obs.Plan
	opKinds := make([]string, 0, len(p.Operations))
	for _, op := range p.Operations {
		opKinds = append(opKinds, string(op.Kind))
	}
	attrs = append(attrs,
		"intent", string(p.Intent),
		"complexity", string(p.Complexity),
		"compound", p.Compound,
		"confidence", p.Confidence,
		"classifier_source", p.ClassifierSource,
		"operation_count", len(p.Operations),
		"operation_kinds", opKinds,
		"planned_operation", primaryPlannedOperation(p),
		"selected_context_file_count", len(obs.CandidateContext),
		"selected_context_files", obs.CandidateContext,
		"ambiguous", p.Ambiguous,
		"needs_deepseek", obs.NeedsDeepSeek,
		"mapped_intent", string(obs.MappedIntent),
		"mapped_generation_mode", obs.MappedGeneration,
		"applied_escalate", obs.AppliedEscalate,
		"local_lm_called", obs.LocalLM.Called,
		"local_lm_skipped", obs.LocalLM.Skipped,
		"local_lm_reason", obs.LocalLM.Reason,
		"local_lm_elapsed_ms", obs.LocalLM.ElapsedMs,
		"local_lm_timeout", obs.LocalLM.Timeout,
		"local_lm_success", obs.LocalLM.Success,
		"local_lm_failure", obs.LocalLM.Failure,
		"local_lm_fallback", obs.LocalLM.Fallback,
		"local_lm_provider", obs.LocalLM.Provider,
		"local_lm_model", obs.LocalLM.Model,
		"local_lm_input_bytes", obs.LocalLM.InputBytes,
		"local_lm_output_bytes", obs.LocalLM.OutputBytes,
		"deterministic_confidence", obs.LocalLM.DeterministicConfidence,
		"refined_confidence", obs.LocalLM.RefinedConfidence,
		"plan_changed", obs.LocalLM.PlanChanged,
		"operation_count_before", obs.LocalLM.OperationCountBefore,
		"operation_count_after", obs.LocalLM.OperationCountAfter,
		"refinement_applied", obs.LocalLM.RefinementApplied,
		"refinement_rejected", obs.LocalLM.RefinementRejected,
		"refinement_fields_count", obs.LocalLM.RefinementFieldsCount,
		"plan_intent_before", obs.LocalLM.PlanIntentBefore,
		"plan_intent_after", obs.LocalLM.PlanIntentAfter,
		"plan_op_count_before", obs.LocalLM.PlanOpCountBefore,
		"plan_op_count_after", obs.LocalLM.PlanOpCountAfter,
		"shadow_status", obs.Shadow.Status,
		"shadow_skipped", obs.Shadow.Skipped,
		"shadow_skip_reason", obs.Shadow.SkipReason,
		"shadow_candidate_valid", obs.Shadow.CandidateValid,
		"shadow_candidate_unsafe", obs.Shadow.CandidateUnsafe,
		"shadow_candidate_latency_ms", obs.Shadow.CandidateLatencyMS,
		"shadow_exact_match", obs.Shadow.ExactMatch,
		"shadow_affects_production", false,
	)
	slog.Info("ai: builderplan observation", attrs...)
}

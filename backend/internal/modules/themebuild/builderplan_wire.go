package themebuild

import (
	"log/slog"
	"time"

	"ai-chat/internal/builderplan"
)

// planObservation is the non-mutating BuilderPlan snapshot used when
// BUILDER_PLAN_ENABLED=true. It never writes theme files or calls DeepSeek.
type planObservation struct {
	Plan              builderplan.BuilderPlan
	Valid             bool
	FallbackReason    string
	PlannerElapsedMs  int64
	ContextElapsedMs  int64
	NeedsDeepSeek     bool
	MappedIntent      Intent // empty when no safe mapping
	MappedGeneration  string // existing generation-mode label for logs
	CandidateContext  []string
	AppliedEscalate   bool
}

// observeBuilderPlan runs CPU-only plan build+validate. On any failure it
// returns Valid=false and FallbackReason so callers keep the existing path.
func observeBuilderPlan(prompt string) planObservation {
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
	// BuildPlan already populated RequiredFiles; keep an explicit measurement
	// of the pure selection helper for pre-model timing logs.
	if len(plan.RequiredFiles) == 0 {
		plan.RequiredFiles = candidates
	}

	obs := planObservation{
		Plan:             plan,
		Valid:            true,
		PlannerElapsedMs: plannerMs,
		ContextElapsedMs: contextMs,
		NeedsDeepSeek:    builderplan.NeedsDeepSeek(plan),
		CandidateContext: plan.RequiredFiles,
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
		// Theme-query / conversation already decided by ClassifyIntent;
		// do not override those special routes from the planner.
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
		"selected_context_file_count", len(obs.CandidateContext),
		"selected_context_files", obs.CandidateContext,
		"ambiguous", p.Ambiguous,
		"needs_deepseek", obs.NeedsDeepSeek,
		"mapped_intent", string(obs.MappedIntent),
		"mapped_generation_mode", obs.MappedGeneration,
		"applied_escalate", obs.AppliedEscalate,
	)
	slog.Info("ai: builderplan observation", attrs...)
}

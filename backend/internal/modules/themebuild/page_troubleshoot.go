package themebuild

import (
	"context"
	"log/slog"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/builderoperations"
	"ai-chat/internal/builderplan"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/themefs"
)

func isPageTroubleshootPlan(plan builderplan.BuilderPlan) bool {
	if plan.Intent == builderplan.IntentPageTroubleshoot {
		return true
	}
	for _, op := range plan.Operations {
		if op.Kind == builderplan.OpDiagnoseExistingPage || op.Kind == builderplan.OpFixExistingPage {
			return true
		}
	}
	return builderoperations.MatchesTroubleshootPrompt(plan.OriginalPrompt)
}

func runLocalDiagnoseExisting(
	ctx context.Context,
	store themefs.ThemeStore,
	auth themefs.RequestAuth,
	prompt string,
	plan *builderplan.BuilderPlan,
) (aiResult *ai.Result, opResult builderoperations.Result, handled bool, err error) {
	name, resolved := builderoperations.Resolve(prompt, plan)
	if !resolved || name != builderoperations.NameDiagnoseExistingPage {
		if plan != nil && isPageTroubleshootPlan(*plan) {
			name = builderoperations.NameDiagnoseExistingPage
		} else {
			return nil, opResult, false, nil
		}
	}
	out, err := builderoperations.Run(ctx, name, builderoperations.Input{
		Prompt: prompt,
		Plan:   plan,
		Store:  store,
		Auth:   auth,
	})
	if err != nil {
		return nil, out, false, err
	}
	if out.Outcome == builderoperations.OutcomeNotApplicable {
		return nil, out, false, nil
	}
	return localOpToAIResult(out), out, true, nil
}

// tryPageTroubleshootLocal runs diagnose/fix and records the assistant reply.
// Returns handled=true when the turn is complete (no DeepSeek).
func (s *Service) tryPageTroubleshootLocal(
	ctx context.Context,
	in GenerateInput,
	c chat.Chat,
	genID string,
	store themefs.ThemeStore,
	storeAuth themefs.RequestAuth,
	planObs planObservation,
	doGenerateStart time.Time,
	workspaceLoaded bool,
	exCap *exampleCapture,
) (handled bool, summary string, hasChanges bool, stagedFiles []writtenFile, err error) {
	if !planObs.Valid || !isPageTroubleshootPlan(planObs.Plan) {
		if !builderoperations.MatchesTroubleshootPrompt(in.Prompt) {
			return false, "", false, nil, nil
		}
	}
	plan := planObs.Plan
	active, _ := s.activeTargets.get(in.TenantID, c.ID)
	slug := resolveTroubleshootSlug(in.Prompt, plan, active)
	targetResolved := slug != ""
	enrichPlanTroubleshootTarget(&plan, slug)

	slog.Info("ai: page troubleshoot detected",
		"generation_id", genID,
		"tenant_id", in.TenantID,
		"chat_id", c.ID,
		"page_troubleshoot_detected", true,
		"page_troubleshoot_target_resolved", targetResolved,
		"page_troubleshoot_active_target", active.Identity,
		"planned_operation", string(builderplan.OpDiagnoseExistingPage),
		"local_lm_called", false)

	aiRes, opOut, ok, runErr := runLocalDiagnoseExisting(ctx, store, storeAuth, in.Prompt, &plan)
	if runErr != nil {
		slog.Warn("ai: page troubleshoot failed",
			"generation_id", genID,
			"error", runErr.Error(),
			"page_diagnosis_elapsed_ms", opOut.Metrics.ElapsedMs)
		return true, "I couldn't check that page right now.", false, nil, nil
	}
	if !ok || aiRes == nil {
		return false, "", false, nil, nil
	}

	issueCount := 0
	fixApplied := false
	if opOut.Diagnosis != nil {
		issueCount = len(opOut.Diagnosis.Issues)
		fixApplied = opOut.Diagnosis.FixApplied
		if opOut.Diagnosis.Slug != "" {
			s.activeTargets.put(in.TenantID, c.ID, activeBuilderTarget{
				Type: "page", Identity: opOut.Diagnosis.Slug, Path: opOut.Diagnosis.Path,
				Operation: string(builderplan.OpDiagnoseExistingPage),
			})
		}
	}

	executed := string(builderplan.OpDiagnoseExistingPage)
	if fixApplied {
		executed = string(builderplan.OpFixExistingPage)
	}
	slog.Info("ai: page diagnosis",
		"generation_id", genID,
		"chat_id", c.ID,
		"page_diagnosis_elapsed_ms", opOut.Metrics.ElapsedMs,
		"page_diagnosis_issue_count", issueCount,
		"page_diagnosis_fix_applied", fixApplied,
		"page_troubleshoot_deepseek_calls", 0,
		"page_troubleshoot_local_lm_calls", 0,
		"planned_operation", string(builderplan.OpDiagnoseExistingPage),
		"executed_operation", executed,
		"plan_execution_mismatch", false,
		"deepseek_called", false,
		"workspace_loaded", workspaceLoaded,
		"duration_ms", time.Since(doGenerateStart).Milliseconds())

	summary = aiRes.Summary
	if summary == "" {
		summary = "Done."
	}
	if exCap != nil {
		exCap.localOpName = builderoperations.NameDiagnoseExistingPage
		exCap.needsClarify = aiRes.NeedsClarification
	}

	if !proposalHasChanges(aiRes) {
		return true, summary, false, nil, nil
	}

	unlock, lockErr := s.themeLocks.Lock(ctx, themeLockKey(in.TenantID, in.ThemeSlug))
	if lockErr != nil {
		return true, "I couldn't complete that fix.", false, nil, nil
	}
	defer unlock()

	planWrite, planErr := s.buildWritePlan(ctx, store, storeAuth, aiRes)
	if planErr != nil {
		slog.Warn("ai: page troubleshoot stage failed", "generation_id", genID, "error", planErr.Error())
		return true, "I found an issue but couldn't apply a safe fix automatically.", false, nil, nil
	}
	staged := planToStaged(planWrite)
	return true, summary, true, staged, nil
}

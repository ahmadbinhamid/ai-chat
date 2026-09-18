package themebuild

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/themecheck"
	"ai-chat/internal/themefs"
)

// errCompoundPartial is returned when later compound steps fail after earlier
// ones produced a usable draft. doGenerate stages Accum then surfaces Msg.
type errCompoundPartial struct {
	Accum      *ai.Result
	Registries []*themefs.PageEntry
	Msg        string
	Cause      error
}

func (e *errCompoundPartial) Error() string {
	if e == nil {
		return "compound partial failure"
	}
	if e.Msg != "" {
		return e.Msg
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "compound partial failure"
}

func (e *errCompoundPartial) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// runCompoundMultiPageCreate executes PlanCompoundWorkflow steps independently.
// Successful steps merge into one draft; a later failure keeps prior pages and
// returns *errCompoundPartial (never regenerates completed steps).
//
// Canonical per-create-step contract: one pages/<slug>.liquid create +
// page_registry_entry (preferred) or pages.json append. Registries accumulate
// across steps for buildWritePlan(extraEntries) — no full pages.json rewrite
// required from the model.
func (s *Service) runCompoundMultiPageCreate(
	ctx context.Context,
	in GenerateInput,
	c chat.Chat,
	genID string,
	baseTC ai.ThemeContext,
	store themefs.ThemeStore,
	storeAuth themefs.RequestAuth,
	snapBase themecheck.Snapshot,
	readFile ai.FileReader,
	emitter *eventEmitter,
) (*ai.Result, []ai.Turn, []*themefs.PageEntry, error) {
	plan, ok := PlanCompoundWorkflow(in.Prompt)
	if !ok {
		return nil, nil, nil, fmt.Errorf("compound plan unavailable")
	}

	slog.Info("ai: compound workflow start",
		"chat_id", c.ID, "generation_id", genID,
		"steps", len(plan.Steps), "kinds", compoundStepKinds(plan))

	progress := CompoundProgress{}
	var turns []ai.Turn
	basePagesJSON := baseTC.PagesJSON
	if strings.TrimSpace(basePagesJSON) == "" {
		if raw, err := store.ReadFile(ctx, storeAuth, "pages.json"); err == nil {
			basePagesJSON = raw
		}
	}
	// pagesJSON tracks the latest registry view for step context (identity list).
	// Merges are always applied against basePagesJSON + all registries at the end
	// so a failed step never persists a partial destructive rewrite.
	pagesJSON := basePagesJSON

	for i := range plan.Steps {
		step := plan.Steps[i]
		if err := ctx.Err(); err != nil {
			return compoundPartialOrErr(progress, turns, basePagesJSON, err)
		}

		emitter.emit(ctx, EventTypePreparingAI, map[string]any{
			"compound_step": step.ID, "label": step.Label, "kind": step.Kind,
		})

		stepTC := baseTC
		stepTC.PageCreatePrepared = true
		stepTC.PageCreateAllowRead = false
		stepTC.CompoundAtomicCreate = step.Kind == CompoundStepCreatePage
		stepTC.MaxToolIterations = maxComplexPageModelCalls
		if stepTC.MaxTokensOverride <= 0 || stepTC.MaxTokensOverride > ai.DefaultTokenBudgets().Complex {
			stepTC.MaxTokensOverride = ai.DefaultTokenBudgets().Complex
		}
		stepTC.FirstTokenTimeoutOverride = ai.FirstTokenTimeoutForMode(ai.FirstTokenModeComplex)
		stepTC.StreamIdleTimeoutOverride = ai.PreparedStreamIdleTimeout()
		stepTC.StreamMaxAttemptsOverride = 0 // keep default; do not amplify retries per step
		stepTC.FileTree = filterFileTreeToPaths(stepTC.FileTree, []string{"pages.json", "defaults.json"})

		prepared := compoundStepPreparedPrompt(step, plan.OriginalPrompt, pagesJSON, progress.Accum)
		stepIn := in
		// Merchant validators must see the atomic step scope — never the
		// original "create N pages" prompt (that trips the batch pages.json gate).
		stepIn.Prompt = step.ValidatorPrompt
		stepToolExec := s.buildToolExecutor(store, storeAuth, stepTC, snapBase)

		result, stepTurns, err := s.generateValidProposal(ctx, stepTC, nil, prepared, stepToolExec, readFile, emitter, stepIn)
		turns = append(turns, stepTurns...)
		if err != nil {
			progress.Failed = &plan.Steps[i]
			return compoundPartialOrErr(progress, turns, basePagesJSON, err)
		}

		if RejectOversizedMultiPageIndexRewrite(plan.OriginalPrompt, result) != nil {
			slog.Warn("ai: compound step stripped index rewrite",
				"generation_id", genID, "step", step.ID, "paths", proposalPaths(result))
			result = StripMultiPageIndexRewrites(result)
		}

		if step.Kind == CompoundStepCreatePage {
			result, err = prepareCompoundCreateStep(result, pagesJSON)
			if err != nil {
				nudge := fmt.Sprintf(
					"That atomic step was incomplete: %s. Resubmit ONE new pages/<slug>.liquid create plus page_registry_entry ONLY. FORBIDDEN: rewriting pages.json / blog.liquid / home.liquid.",
					err)
				turns = append(turns,
					ai.Turn{Role: "assistant", Content: recapAssistantTurn(result)},
					ai.Turn{Role: "user", Content: nudge},
				)
				result, stepTurns, err = s.generateValidProposal(ctx, stepTC, turns, nudge+"\nCall propose_changes only.", stepToolExec, readFile, emitter, stepIn)
				turns = append(turns, stepTurns...)
				if err != nil {
					progress.Failed = &plan.Steps[i]
					return compoundPartialOrErr(progress, turns, basePagesJSON, err)
				}
				result = StripMultiPageIndexRewrites(result)
				result, err = prepareCompoundCreateStep(result, pagesJSON)
				if err != nil {
					progress.Failed = &plan.Steps[i]
					return compoundPartialOrErr(progress, turns, basePagesJSON, err)
				}
			}
			if err := incompleteAtomicPageCreateProposal(result); err != nil {
				nudge := fmt.Sprintf(
					"That atomic step was incomplete: %s. Resubmit ONE new pages/<slug>.liquid create plus page_registry_entry ONLY. FORBIDDEN: rewriting pages.json / blog.liquid / home.liquid.",
					err)
				turns = append(turns,
					ai.Turn{Role: "assistant", Content: recapAssistantTurn(result)},
					ai.Turn{Role: "user", Content: nudge},
				)
				result, stepTurns, err = s.generateValidProposal(ctx, stepTC, turns, nudge+"\nCall propose_changes only.", stepToolExec, readFile, emitter, stepIn)
				turns = append(turns, stepTurns...)
				if err != nil {
					progress.Failed = &plan.Steps[i]
					return compoundPartialOrErr(progress, turns, basePagesJSON, err)
				}
				result = StripMultiPageIndexRewrites(result)
				result, err = prepareCompoundCreateStep(result, pagesJSON)
				if err != nil {
					progress.Failed = &plan.Steps[i]
					return compoundPartialOrErr(progress, turns, basePagesJSON, fmt.Errorf("proposal/tool contract mismatch: %w", err))
				}
				if err := incompleteAtomicPageCreateProposal(result); err != nil {
					progress.Failed = &plan.Steps[i]
					return compoundPartialOrErr(progress, turns, basePagesJSON, fmt.Errorf("proposal/tool contract mismatch: %w", err))
				}
			}
		}

		if step.Kind == CompoundStepAddToMenu {
			if err := incompleteAddToMenuProposal(plan.OriginalPrompt, result); err != nil {
				progress.Failed = &plan.Steps[i]
				return compoundPartialOrErr(progress, turns, basePagesJSON, fmt.Errorf("invalid model proposal: %w", err))
			}
		}

		if proposalHasChanges(result) {
			emitter.emit(ctx, EventTypeProposing, map[string]any{
				"file_count": len(result.Files), "compound_step": step.ID, "label": step.Label,
			})
			snap := s.buildSnapshot(ctx, store, storeAuth, snapBase, result)
			var repairErr error
			result, _, repairErr = s.checkAndRepair(ctx, stepIn, c.ID, stepTC, turns, result, snap, stepToolExec, readFile, emitter)
			if repairErr != nil {
				progress.Failed = &plan.Steps[i]
				return compoundPartialOrErr(progress, turns, basePagesJSON, repairErr)
			}
			// Repair may reintroduce pages.json — strip again for create steps.
			if step.Kind == CompoundStepCreatePage {
				result, _ = stripModelPagesJSON(result)
			}
		}

		progress.Accum = MergeCompoundResults(progress.Accum, result)
		progress.Registries = accumulateCompoundRegistry(progress.Registries, result.PageRegistryEntry)
		progress.Completed = append(progress.Completed, step)
		if result.PageRegistryEntry != nil {
			pagesJSON = upsertPagesJSONWithRegistry(pagesJSON, result.PageRegistryEntry)
		}

		slog.Info("ai: compound step done",
			"generation_id", genID, "step", step.ID, "kind", step.Kind,
			"label", step.Label, "files", len(result.Files),
			"accum_files", len(progress.Accum.Files),
			"registries", len(progress.Registries))
	}

	if progress.Accum == nil {
		return &ai.Result{Summary: "No changes were produced."}, turns, nil, nil
	}
	// Deterministic pages.json merge from live base + all step registries.
	// Never stage model-authored pages.json for compound create.
	progress.Accum, _ = stripModelPagesJSON(progress.Accum)
	if len(progress.Registries) > 0 {
		if err := injectMergedPagesJSON(progress.Accum, basePagesJSON, progress.Registries); err != nil {
			progress.Failed = &plan.Steps[len(plan.Steps)-1]
			return compoundPartialOrErr(progress, turns, basePagesJSON, err)
		}
	}
	progress.Accum.Summary = compoundSuccessSummary(progress)
	return progress.Accum, turns, progress.Registries, nil
}

func compoundPartialOrErr(progress CompoundProgress, turns []ai.Turn, basePagesJSON string, err error) (*ai.Result, []ai.Turn, []*themefs.PageEntry, error) {
	msg := CompoundPartialFailureMessage(progress, err)
	if progress.Accum != nil && len(progress.Accum.Files) > 0 {
		progress.Accum, _ = stripModelPagesJSON(progress.Accum)
		if len(progress.Registries) > 0 {
			if mergeErr := injectMergedPagesJSON(progress.Accum, basePagesJSON, progress.Registries); mergeErr != nil {
				slog.Warn("ai: compound partial registry merge failed", "error", mergeErr)
			}
		}
		progress.Accum.Summary = msg
		return progress.Accum, turns, progress.Registries, &errCompoundPartial{
			Accum: progress.Accum, Registries: progress.Registries, Msg: msg, Cause: err,
		}
	}
	return nil, turns, progress.Registries, fmt.Errorf("%s: %w", msg, err)
}

func compoundStepKinds(plan CompoundPlan) []string {
	out := make([]string, len(plan.Steps))
	for i, s := range plan.Steps {
		out[i] = s.Kind
	}
	return out
}

func compoundSuccessSummary(progress CompoundProgress) string {
	parts := make([]string, 0, len(progress.Completed))
	for _, s := range progress.Completed {
		parts = append(parts, s.Label)
	}
	return "Completed: " + strings.Join(parts, "; ") + "."
}

func pagesJSONFromResult(result *ai.Result) string {
	if result == nil {
		return ""
	}
	for _, f := range result.Files {
		if strings.EqualFold(strings.TrimSpace(f.Path), "pages.json") && strings.TrimSpace(f.Content) != "" {
			return f.Content
		}
	}
	return ""
}

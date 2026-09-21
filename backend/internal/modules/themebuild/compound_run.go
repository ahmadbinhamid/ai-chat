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
	// ThemeContext.PagesJSON may be a truncated prompt stub (context-plan /
	// simple-edit truncate). Never use it as the registry merge base — that
	// produced: parse pages.json: invalid character '\n' in string literal.
	storePagesJSON := ""
	if raw, err := store.ReadFile(ctx, storeAuth, "pages.json"); err == nil {
		storePagesJSON = raw
	}
	basePagesJSON, baseErr := resolveCanonicalPagesJSON(storePagesJSON, "")
	if baseErr != nil {
		return nil, nil, nil, baseErr
	}
	pagesJSON := basePagesJSON
	progress.RegistryJSON = basePagesJSON

	storeDefaultsJSON := ""
	if raw, err := store.ReadFile(ctx, storeAuth, pathDefaultsJSON); err == nil {
		storeDefaultsJSON = raw
	}
	baseDefaultsJSON, defaultsErr := resolveCanonicalDefaultsJSON(storeDefaultsJSON, "")
	if defaultsErr != nil {
		// Menu step needs a valid base; create-only compounds can still proceed
		// and fail safely at the menu step if defaults are broken.
		slog.Warn("ai: compound defaults.json base unavailable",
			"generation_id", genID, "error", defaultsErr)
	}

	for i := range plan.Steps {
		step := plan.Steps[i]
		if err := ctx.Err(); err != nil {
			return compoundPartialOrErr(progress, turns, progress.RegistryJSON, err)
		}

		emitter.emit(ctx, EventTypePreparingAI, map[string]any{
			"compound_step": step.ID, "label": step.Label, "kind": step.Kind,
		})

		var result *ai.Result
		var stepTurns []ai.Turn
		var err error

		if step.Kind == CompoundStepAddToMenu {
			// Structured deterministic menu merge — never call DeepSeek for a
			// full defaults.json rewrite (live failure: incomplete proposal).
			menuBase := baseDefaultsJSON
			if strings.TrimSpace(progress.MenuJSON) != "" {
				menuBase = progress.MenuJSON
			}
			if strings.TrimSpace(menuBase) == "" {
				progress.Failed = &plan.Steps[i]
				return compoundPartialOrErr(progress, turns, progress.RegistryJSON,
					fmt.Errorf("menu operation: current defaults.json invalid: empty"))
			}
			mergedDefaults, summary, menuErr := runDeterministicAddToMenu(menuBase, progress.Registries)
			if menuErr != nil {
				// Keep any successfully merged prefix (menu #1 of 2) as checkpoint.
				if strings.TrimSpace(mergedDefaults) != "" && isValidDefaultsJSON(mergedDefaults) && mergedDefaults != menuBase {
					progress.MenuJSON = mergedDefaults
				}
				progress.Failed = &plan.Steps[i]
				slog.Warn("ai: compound menu checkpoint rejected",
					"generation_id", genID, "step", step.ID, "error", menuErr)
				return compoundPartialOrErr(progress, turns, progress.RegistryJSON, menuErr)
			}
			progress.MenuJSON = mergedDefaults
			result = &ai.Result{
				Summary: summary,
				Files: []ai.GeneratedFile{{
					Path: pathDefaultsJSON, Action: "update", Content: mergedDefaults,
				}},
			}
			slog.Info("ai: compound menu merge done",
				"generation_id", genID, "step", step.ID,
				"pages", len(progress.Registries), "deepseek", 0)
		} else {
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

			result, stepTurns, err = s.generateValidProposal(ctx, stepTC, nil, prepared, stepToolExec, readFile, emitter, stepIn)
			turns = append(turns, stepTurns...)
			if err != nil {
				progress.Failed = &plan.Steps[i]
				return compoundPartialOrErr(progress, turns, progress.RegistryJSON, err)
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
						return compoundPartialOrErr(progress, turns, progress.RegistryJSON, err)
					}
					result = StripMultiPageIndexRewrites(result)
					result, err = prepareCompoundCreateStep(result, pagesJSON)
					if err != nil {
						progress.Failed = &plan.Steps[i]
						return compoundPartialOrErr(progress, turns, progress.RegistryJSON, err)
					}
				}
				synthesizeMissingPageRegistry(result)
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
						return compoundPartialOrErr(progress, turns, progress.RegistryJSON, err)
					}
					result = StripMultiPageIndexRewrites(result)
					result, err = prepareCompoundCreateStep(result, pagesJSON)
					if err != nil {
						progress.Failed = &plan.Steps[i]
						return compoundPartialOrErr(progress, turns, progress.RegistryJSON, fmt.Errorf("proposal/tool contract mismatch: %w", err))
					}
					if err := incompleteAtomicPageCreateProposal(result); err != nil {
						progress.Failed = &plan.Steps[i]
						return compoundPartialOrErr(progress, turns, progress.RegistryJSON, fmt.Errorf("proposal/tool contract mismatch: %w", err))
					}
				}
				// Never stage model defaults.json from a create step.
				result, _ = stripModelDefaultsJSON(result)
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
					return compoundPartialOrErr(progress, turns, progress.RegistryJSON, repairErr)
				}
				// Repair may reintroduce pages.json / defaults.json — strip again.
				if step.Kind == CompoundStepCreatePage {
					result, _ = stripModelPagesJSON(result)
					result, _ = stripModelDefaultsJSON(result)
				}
			}
		}

		// Checkpoint registry BEFORE accepting the step into Accum. A failed
		// structured merge rejects only this operation and keeps the prior
		// valid checkpoint.
		if result.PageRegistryEntry != nil {
			nextCP, _, cpErr := applyRegistryEntryCheckpoint(progress.RegistryJSON, result.PageRegistryEntry)
			if cpErr != nil {
				progress.Failed = &plan.Steps[i]
				slog.Warn("ai: compound registry checkpoint rejected",
					"generation_id", genID, "step", step.ID, "error", cpErr)
				return compoundPartialOrErr(progress, turns, progress.RegistryJSON, cpErr)
			}
			progress.RegistryJSON = nextCP
			pagesJSON = nextCP
		}

		progress.Accum = MergeCompoundResults(progress.Accum, result)
		progress.Registries = accumulateCompoundRegistry(progress.Registries, result.PageRegistryEntry)
		progress.Completed = append(progress.Completed, step)

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
	progress.Accum, _ = stripModelDefaultsJSON(progress.Accum)
	if len(progress.Registries) > 0 {
		if err := injectMergedPagesJSON(progress.Accum, basePagesJSON, progress.Registries); err != nil {
			// Fall back to last validated checkpoint (replay already applied).
			if strings.TrimSpace(progress.RegistryJSON) != "" && isValidPagesJSON(progress.RegistryJSON) {
				if injErr := injectCheckpointPagesJSON(progress.Accum, progress.RegistryJSON); injErr != nil {
					progress.Failed = &plan.Steps[len(plan.Steps)-1]
					return compoundPartialOrErr(progress, turns, progress.RegistryJSON, err)
				}
			} else {
				progress.Failed = &plan.Steps[len(plan.Steps)-1]
				return compoundPartialOrErr(progress, turns, progress.RegistryJSON, err)
			}
		}
	}
	if strings.TrimSpace(progress.MenuJSON) != "" && isValidDefaultsJSON(progress.MenuJSON) {
		if injErr := injectCheckpointDefaultsJSON(progress.Accum, progress.MenuJSON); injErr != nil {
			progress.Failed = &plan.Steps[len(plan.Steps)-1]
			return compoundPartialOrErr(progress, turns, progress.RegistryJSON, injErr)
		}
	}
	progress.Accum.Summary = compoundSuccessSummary(progress)
	return progress.Accum, turns, progress.Registries, nil
}

func compoundPartialOrErr(progress CompoundProgress, turns []ai.Turn, basePagesJSON string, err error) (*ai.Result, []ai.Turn, []*themefs.PageEntry, error) {
	msg := CompoundPartialFailureMessage(progress, err)
	if progress.Accum != nil && len(progress.Accum.Files) > 0 {
		progress.Accum, _ = stripModelPagesJSON(progress.Accum)
		progress.Accum, _ = stripModelDefaultsJSON(progress.Accum)
		if strings.TrimSpace(progress.RegistryJSON) != "" && isValidPagesJSON(progress.RegistryJSON) {
			if injErr := injectCheckpointPagesJSON(progress.Accum, progress.RegistryJSON); injErr != nil {
				slog.Warn("ai: compound partial registry checkpoint inject failed", "error", injErr)
			}
		} else if len(progress.Registries) > 0 {
			if mergeErr := injectMergedPagesJSON(progress.Accum, basePagesJSON, progress.Registries); mergeErr != nil {
				slog.Warn("ai: compound partial registry merge failed", "error", mergeErr)
			}
		}
		if strings.TrimSpace(progress.MenuJSON) != "" && isValidDefaultsJSON(progress.MenuJSON) {
			if injErr := injectCheckpointDefaultsJSON(progress.Accum, progress.MenuJSON); injErr != nil {
				slog.Warn("ai: compound partial menu checkpoint inject failed", "error", injErr)
			}
		}
		progress.Accum.Summary = msg
		return progress.Accum, turns, progress.Registries, &errCompoundPartial{
			Accum: progress.Accum, Registries: progress.Registries, Msg: msg, Cause: err,
		}
	}
	return nil, turns, progress.Registries, fmt.Errorf("%s: %w", msg, err)
}

// injectCheckpointPagesJSON writes an already-validated pages.json body.
func injectCheckpointPagesJSON(result *ai.Result, pagesJSON string) error {
	if result == nil {
		return fmt.Errorf("nil result")
	}
	if !isValidPagesJSON(pagesJSON) {
		return fmt.Errorf("parse pages.json: checkpoint is not valid JSON")
	}
	replaced := false
	for i := range result.Files {
		if strings.EqualFold(strings.TrimSpace(result.Files[i].Path), "pages.json") {
			result.Files[i] = ai.GeneratedFile{Path: "pages.json", Action: "update", Content: pagesJSON}
			replaced = true
			break
		}
	}
	if !replaced {
		result.Files = append(result.Files, ai.GeneratedFile{
			Path: "pages.json", Action: "update", Content: pagesJSON,
		})
	}
	return nil
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

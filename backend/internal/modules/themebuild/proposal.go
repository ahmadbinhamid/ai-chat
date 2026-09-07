package themebuild

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/themecheck"
	"ai-chat/internal/themefs"
)

// toProposal maps ai.Result into the minimal shape themecheck.Check needs —
// PageRegistryEntry carries over unchanged since ai.Result already types it
// as *themefs.PageEntry (see themecheck.Proposal's doc comment).
func toProposal(r *ai.Result) themecheck.Proposal {
	files := make([]themecheck.ProposedFile, len(r.Files))
	for i, f := range r.Files {
		files[i] = themecheck.ProposedFile{Path: f.Path, Action: f.Action, Content: f.Content}
	}
	return themecheck.Proposal{
		Files:              files,
		PageRegistryEntry:  r.PageRegistryEntry,
		LayoutLinksToAdd:   r.LayoutLinksToAdd,
		LayoutScriptsToAdd: r.LayoutScriptsToAdd,
	}
}

// proposalHasChanges reports whether result proposes anything to write —
// shared by doGenerate (deciding whether to run themecheck/buildWritePlan at
// all) and its post-repair recheck (a retry that ends in NeedsClarification
// legitimately has nothing left to write).
func proposalHasChanges(result *ai.Result) bool {
	return len(result.Files) > 0 || result.PageRegistryEntry != nil ||
		len(result.LayoutLinksToAdd) > 0 || len(result.LayoutScriptsToAdd) > 0
}

// clearIfNeedsClarification defensively drops any changes the model
// proposed despite the system prompt's instruction not to when asking a
// clarifying question — a clarification turn has nothing to apply.
func clearIfNeedsClarification(result *ai.Result) {
	if !result.NeedsClarification {
		return
	}
	result.Files = nil
	result.PageRegistryEntry = nil
	result.LayoutLinksToAdd = nil
	result.LayoutScriptsToAdd = nil
}

// emptyProposalFallbackSummary replaces the model's own summary when an
// unexplored, empty proposal (see isUnexploredEmptyProposal) survives every
// retry — the merchant-facing admission that nothing happened, instead of
// the model's own fabricated description of work it never did.
const emptyProposalFallbackSummary = "I wasn't able to make that change — try rephrasing, or be more specific about which page or section you mean."

// isUnexploredEmptyProposal reports whether result is the hallucinated-
// success shape this whole mechanism exists to catch: needs_clarification
// is false (the model isn't correctly signaling "nothing to change" the
// documented way), proposalHasChanges is false (no files, no page
// registration, no layout links — genuinely nothing proposed), AND the
// model made zero exploration tool calls (list_theme_files/read_theme_file/
// grep_theme — see ai.Result.ExplorationToolCalls) before proposing.
//
// That last condition is the actual distinguishing rule, and it's the part
// that matters: reading nothing isn't proof of a hallucination by itself —
// a trivial request could legitimately need no exploration — but a model
// that explored NOTHING and still describes specific work ("added an
// animated hero, a sticky sidebar...") is fabricating, while a model that
// read the relevant files and THEN concluded there's nothing to change (a
// real "that's already true" or "that's out of scope" answer — see the
// out_of_scope/unrelated_technical_question eval tasks) is behaving
// reasonably and its own summary is trustworthy. Zero exploration is the
// one signal available in an ai.Result that separates the two without
// flagging every legitimate empty answer along with the real hallucination.
func isUnexploredEmptyProposal(result *ai.Result) bool {
	return !result.NeedsClarification && !proposalHasChanges(result) && result.ExplorationToolCalls == 0
}

// generateValidProposal makes doGenerate's very first Generate call and
// retries it, up to maxThemeCheckRetries times, if the reply fails
// validateProposal — the same bounded treatment checkAndRepair's own retry
// loop applies to a malformed *repair* reply (see its invalid-proposal
// branch), now covering the first attempt too: a garbled first reply (a bad
// path, a corrupted field) is model flakiness observed in production, not a
// reason to hard-fail the whole generation with zero retries. Returns the
// (possibly extended) turns history so a later checkAndRepair call sees any
// corrective exchange that happened here, instead of silently dropping it.
func (s *Service) generateValidProposal(
	ctx context.Context,
	tc ai.ThemeContext,
	turns []ai.Turn,
	prompt string,
	toolExec ai.ToolExecutor,
	readFile ai.FileReader,
	emitter *eventEmitter,
	in GenerateInput,
) (*ai.Result, []ai.Turn, error) {
	nextPrompt := prompt
	// Only for the two Warn lines below — emitter's own emit is already
	// nil-safe, but a direct field read on a nil *eventEmitter (tests pass
	// nil — see generate_valid_proposal_test.go) is not.
	chatID := ""
	if emitter != nil {
		chatID = emitter.chatID
	}

	// attempt counts total Generate calls made here, including the first —
	// mirrors checkAndRepair's own budget: maxThemeCheckRetries+1 total
	// calls (the original attempt plus this many retries), independent of
	// checkAndRepair's own separate retry budget for themecheck rejections
	// (see doGenerate's structure: these are two distinct stages). Shared,
	// not duplicated, by the invalid-proposal retry below AND the
	// unexplored-empty-proposal retry further down — see
	// isUnexploredEmptyProposal's own doc comment for why an otherwise-valid
	// but suspiciously empty proposal needs its own check here rather than
	// being accepted as a real answer.
	for attempt := 1; ; attempt++ {
		result, genErr := s.gen.Generate(ctx, tc, turns, nextPrompt, onThinkingDelta(ctx, emitter), toolProgressFor(ctx, emitter), toolExec, readFile)
		if genErr != nil {
			// A hard API/transport error is a different failure mode from an
			// invalid proposal — already handled by the caller/reaper, not
			// retried here.
			return nil, turns, genErr
		}

		clearIfNeedsClarification(result)

		if err := validateProposal(result, tc.GenerationMode); err != nil {
			if attempt >= maxThemeCheckRetries+1 {
				return nil, turns, fmt.Errorf("invalid model proposal: %w", err)
			}
			slog.Warn("initial generation produced an invalid proposal, retrying if budget remains",
				"tenant_id", in.TenantID, "theme_slug", in.ThemeSlug, "attempt", attempt, "error", err)
			emitter.emit(ctx, EventTypeCheckFailed, map[string]any{
				"attempt": attempt, "message": "invalid model proposal: " + err.Error(),
			})
			turns = append(turns,
				ai.Turn{Role: "assistant", Content: recapAssistantTurn(result)},
				ai.Turn{Role: "user", Content: fmt.Sprintf(
					"That reply wasn't valid: %s. Resubmit a corrected, complete proposal (not a diff), "+
						"following the earlier instructions exactly. Never invent a placeholder path or "+
						"partial content — if you don't have a complete, verified proposal ready, call "+
						"propose_changes with needs_clarification: true, files: [], and explain why instead "+
						"of guessing. Action \"edit\" is fine for this — it isn't a diff, it still produces the "+
						"complete corrected file, just via old_string/new_string instead of retyping it whole.", err)},
			)
			nextPrompt = "Please resubmit a corrected, complete proposal as instructed above."
			continue
		}

		if isUnexploredEmptyProposal(result) {
			if attempt >= maxThemeCheckRetries+1 {
				// Fail open, per this whole mechanism's own rule: never turn
				// a working generation into a failed one. The merchant sees
				// an honest "nothing happened" instead of the model's own
				// fabricated summary — see emptyProposalFallbackSummary.
				// Replacing it HERE (not further down doGenerate) is what
				// keeps the chat transcript consistent: whatever gets
				// recorded as the assistant message is exactly result.Summary
				// from this point on, nothing downstream ever sees the
				// original fabricated text.
				slog.Warn("generateValidProposal: empty proposal with no exploration survived every retry, replacing summary with an honest fallback",
					"tenant_id", in.TenantID, "theme_slug", in.ThemeSlug, "chat_id", chatID, "attempts_used", attempt)
				result.Summary = emptyProposalFallbackSummary
				return result, turns, nil
			}
			slog.Warn("generateValidProposal: empty proposal with no exploration, retrying if budget remains",
				"tenant_id", in.TenantID, "theme_slug", in.ThemeSlug, "chat_id", chatID, "attempt", attempt)
			emitter.emit(ctx, EventTypeCheckFailed, map[string]any{
				"attempt": attempt, "message": "proposal described changes but made no changes and explored no files",
			})
			turns = append(turns,
				ai.Turn{Role: "assistant", Content: recapAssistantTurn(result)},
				ai.Turn{Role: "user", Content: "Your last reply described a change but proposed an empty files array " +
					"without reading or exploring any theme files first. If you have a real change to make, read the " +
					"relevant files (or use grep_theme/list_theme_files to find them) and propose it fully. If there " +
					"is genuinely nothing to change for this request, call propose_changes again with " +
					"needs_clarification: true, files: [], and a summary explaining why — never describe changes " +
					"that were not made."},
			)
			nextPrompt = "Please try again as instructed above."
			continue
		}

		// Distinguishes a first-try success from one that only passed after
		// retrying an invalid proposal — see theory 4 (retries) in the
		// diagnostics task this instruments.
		slog.Info("generateValidProposal succeeded", "tenant_id", in.TenantID, "theme_slug", in.ThemeSlug, "attempts_used", attempt)
		return result, turns, nil
	}
}

// checkAndRepair validates result against snap via themecheck.Check. A
// blocking (error-severity) finding is fed back to the model as a new
// assistant/user turn pair and retried, up to maxThemeCheckRetries times;
// a proposal that never passes fails the generation with a merchant-
// friendly message. Token usage from every retry is folded into the
// accepted result's totals, so RecordAssistantMessage still bills/records
// the full cost of this turn, not just its last attempt. The accepted
// result's warning findings are returned alongside it — never blocking,
// just surfaced (see doGenerate's appendWarningsNote).
func (s *Service) checkAndRepair(
	ctx context.Context,
	in GenerateInput,
	chatID string,
	tc ai.ThemeContext,
	history []ai.Turn,
	result *ai.Result,
	snap themecheck.Snapshot,
	toolExec ai.ToolExecutor,
	readFile ai.FileReader,
	emitter *eventEmitter,
) (*ai.Result, []themecheck.Finding, error) {
	turns := append([]ai.Turn(nil), history...)
	totalInput, totalOutput := result.InputTokens, result.OutputTokens

	for attempt := 1; ; attempt++ {
		// Best-effort: recorded so retry frequency is measurable via the
		// generations table, not just logs — never worth failing the whole
		// generation over. s.repo is nil only in tests that construct a
		// Service directly around a fake generator (see check_and_repair_test.go).
		if s.repo != nil {
			if err := s.repo.SetGenerationAttempts(ctx, chatID, attempt); err != nil {
				slog.Warn("failed to record generation attempt count", "chat_id", chatID, "error", err)
			}
		}

		emitter.emit(ctx, EventTypeChecking, map[string]int{"attempt": attempt})
		findings := themecheck.Check(toProposal(result), snap)
		// Only the raw error COUNT is needed here, to gate the auto-fixer
		// block below — the real errorFindings/warningFindings that drive
		// this attempt's repair decision are computed once, after
		// filtering, further down.
		rawErrorFindings, _ := splitFindings(findings)

		// A missing layout-start/layout-end render is mechanical, not a
		// judgment call — the required text is fixed and known, so patch it
		// in directly rather than spending a whole model round-trip asking
		// for something it has already failed to add correctly at least
		// twice in production (see AutoFixMissingBoilerplate's doc comment).
		// Free (no extra Generate call): just re-run Check on the patched
		// content before deciding whether a real repair round-trip is
		// needed at all.
		if len(rawErrorFindings) > 0 {
			fixedAny := false
			if fixedContent, any := themecheck.AutoFixMissingBoilerplate(toProposal(result)); any {
				for i, f := range result.Files {
					if patched, ok := fixedContent[f.Path]; ok {
						result.Files[i].Content = patched
					}
				}
				fixedAny = true
			}
			// A proposed css/js file with no matching <link>/<script>
			// registration is equally mechanical — see
			// themecheck.AutoFixMissingAssetRegistration's doc comment.
			if links, scripts, any := themecheck.AutoFixMissingAssetRegistration(toProposal(result), snap); any {
				result.LayoutLinksToAdd = append(result.LayoutLinksToAdd, links...)
				result.LayoutScriptsToAdd = append(result.LayoutScriptsToAdd, scripts...)
				fixedAny = true
			}
			// A hardcoded color the model could have reached for a real
			// token instead is the most common single repair trigger in
			// production, and the most expensive one to send back to the
			// model — fixing six colors means re-emitting every touched
			// file in full. See themecheck.AutoFixThemeTokens' own doc
			// comment. Runs against `findings`, the SAME pre-auto-fixer
			// Check() result rawErrorFindings above was split from —
			// deliberately not re-Check()'d against the two fixers above
			// first, because neither touches a .css file's Content
			// (boilerplate only rewrites pages/*.liquid; asset
			// registration only appends to LayoutLinksToAdd/
			// LayoutScriptsToAdd), so the theme-token findings already
			// computed above are still exactly accurate either way.
			if fixedContent, any := themecheck.AutoFixThemeTokens(toProposal(result), snap, findings); any {
				for i, f := range result.Files {
					if patched, ok := fixedContent[f.Path]; ok {
						result.Files[i].Content = patched
					}
				}
				fixedAny = true
			}
			if fixedAny {
				findings = themecheck.Check(toProposal(result), snap)
			}
		}

		// Downgrade findings the merchant's own theme already had before
		// this proposal touched the file — see
		// themecheck.DowngradePreExistingFindings's own doc comment for the
		// matching rule and why it's deliberately biased toward
		// "pre-existing" (the Trustpilot-widget incident this exists to
		// prevent). Deliberately AFTER the auto-fixer block above (which
		// gates on rawErrorFindings, not this), not before: both auto-fixers
		// decide whether to run off the RAW error count, independent of
		// which specific findings caused it — filtering first would risk
		// zeroing that count down to 0 on a proposal that still has a
		// genuine missing-boilerplate/asset-registration problem, skipping a
		// free fix it would otherwise have gotten. snap.Files is the
		// baseline source (see buildSnapshot, which fetches each "update"
		// file's pre-change content into it) — snap itself is computed once
		// before this whole retry loop starts, so every attempt here checks
		// against the ORIGINAL pre-generation content, never a prior failed
		// attempt's own output.
		findings = themecheck.DowngradePreExistingFindings(findings, toProposal(result), snap.Files)
		errorFindings, warningFindings := splitFindings(findings)

		if len(errorFindings) == 0 {
			if attempt > 1 {
				slog.Info("themecheck accepted proposal after retry",
					"tenant_id", in.TenantID, "theme_slug", in.ThemeSlug, "attempt", attempt, "warning_count", len(warningFindings))
			}
			// Unconditional (unlike the log above, which only fires on
			// attempt > 1) so a first-try success is distinguishable from a
			// retried one in the logs — see theory 4 in the diagnostics task
			// this instruments.
			slog.Info("checkAndRepair succeeded", "tenant_id", in.TenantID, "theme_slug", in.ThemeSlug, "attempts_used", attempt)
			result.InputTokens, result.OutputTokens = totalInput, totalOutput
			return result, warningFindings, nil
		}

		slog.Warn("themecheck rejected proposal",
			"tenant_id", in.TenantID, "theme_slug", in.ThemeSlug, "attempt", attempt,
			"error_count", len(errorFindings), "rules", findingRules(errorFindings))
		emitter.emit(ctx, EventTypeCheckFailed, map[string]any{"findings": errorFindings, "attempt": attempt})

		if attempt > maxThemeCheckRetries {
			return nil, nil, fmt.Errorf("the generated changes didn't pass validation after %d attempts: %s",
				attempt, summarizeFindings(errorFindings))
		}

		emitter.emit(ctx, EventTypeRepairing, map[string]int{"attempt": attempt})
		turns = append(turns, ai.Turn{Role: "assistant", Content: recapAssistantTurn(result)})
		repair := repairPrompt(errorFindings)

		repairStart := time.Now()
		retried, genErr := s.gen.Generate(ctx, tc, turns, repair, onThinkingDelta(ctx, emitter), toolProgressFor(ctx, emitter), toolExec, readFile)
		repairElapsed := time.Since(repairStart)
		if genErr != nil {
			// Surfaced distinctly from the generic reaper cleanup: without
			// this, a repair call that runs out the remaining generateTimeout
			// budget (ctx canceled mid-call) produces no log of its own —
			// the chat just sits on "repairing" until the reaper's 1-minute
			// sweep marks it failed, with nothing in the logs explaining why.
			slog.Error("repair generation failed", "tenant_id", in.TenantID, "theme_slug", in.ThemeSlug,
				"attempt", attempt, "elapsed", repairElapsed, "error", genErr)
			return nil, nil, fmt.Errorf("retry generation: %w", genErr)
		}
		slog.Info("repair generation completed", "tenant_id", in.TenantID, "theme_slug", in.ThemeSlug,
			"attempt", attempt, "elapsed", repairElapsed, "input_tokens", retried.InputTokens, "output_tokens", retried.OutputTokens)
		totalInput += retried.InputTokens
		totalOutput += retried.OutputTokens
		turns = append(turns, ai.Turn{Role: "user", Content: repair})

		clearIfNeedsClarification(retried)
		if err := validateProposal(retried, tc.GenerationMode); err != nil {
			// A malformed repair reply (garbled path, corrupted JSON field,
			// etc.) is model flakiness, not necessarily a dead end — the
			// SAME retry budget that governs themecheck rejections should
			// cover this too, instead of burning the whole generation on
			// one bad roll of the dice. `result` (the last themecheck-
			// rejected-but-well-formed proposal) is deliberately left
			// unchanged here so the next loop iteration re-runs Check on
			// it, which reproduces the original rejection and asks for
			// another repair — this is the same bounded loop, not a new one.
			slog.Warn("repair produced an invalid proposal, discarding and retrying if budget remains",
				"tenant_id", in.TenantID, "theme_slug", in.ThemeSlug, "attempt", attempt, "error", err)
			if attempt >= maxThemeCheckRetries {
				return nil, nil, fmt.Errorf("invalid model proposal (retry %d): %w", attempt, err)
			}
			emitter.emit(ctx, EventTypeCheckFailed, map[string]any{
				"attempt": attempt, "message": "repair produced an invalid proposal: " + err.Error(),
			})
			turns = append(turns, ai.Turn{Role: "user", Content: fmt.Sprintf(
				"That reply wasn't valid: %s. Resubmit a corrected, complete proposal (not a diff), "+
					"following the earlier instructions exactly. Never invent a placeholder path or "+
					"partial content — if you don't have a complete, verified proposal ready, call "+
					"propose_changes with needs_clarification: true, files: [], and explain why instead "+
					"of guessing. Action \"edit\" is fine for this — it isn't a diff, it still produces the "+
					"complete corrected file, just via old_string/new_string instead of retyping it whole.", err)})
			continue
		}
		result = retried
	}
}

func splitFindings(findings []themecheck.Finding) (errorFindings, warningFindings []themecheck.Finding) {
	for _, f := range findings {
		if f.Severity == themecheck.SeverityError {
			errorFindings = append(errorFindings, f)
		} else {
			warningFindings = append(warningFindings, f)
		}
	}
	return errorFindings, warningFindings
}

func findingRules(findings []themecheck.Finding) []string {
	rules := make([]string, len(findings))
	for i, f := range findings {
		rules[i] = f.Rule
	}
	return rules
}

func summarizeFindings(findings []themecheck.Finding) string {
	parts := make([]string, len(findings))
	for i, f := range findings {
		if f.Path != "" {
			parts[i] = fmt.Sprintf("[%s] %s: %s", f.Rule, f.Path, f.Message)
		} else {
			parts[i] = fmt.Sprintf("[%s] %s", f.Rule, f.Message)
		}
	}
	return strings.Join(parts, "; ")
}

// recapAssistantTurn replays a rejected proposal's file content back to the
// model as its own prior turn. Without this the model retrying would have
// no memory of what it just wrote: a rejected proposal is never written to
// disk or the chat_generated_files audit trail (Check runs before
// buildWritePlan), so buildEditingFilesContext's real-file grounding won't
// have it either.
func recapAssistantTurn(result *ai.Result) string {
	var b strings.Builder
	if result.Summary != "" {
		fmt.Fprintf(&b, "%s\n\n", result.Summary)
	}
	for _, f := range result.Files {
		fmt.Fprintf(&b, "### %s (%s)\n%s\n\n", f.Path, f.Action, f.Content)
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		out = "(no files proposed)"
	}
	return out
}

// repairPrompt is the new user turn sent back to the model after a rejected
// proposal — every error finding, since those are what actually blocked the
// write (warnings are surfaced to the merchant, never fed back for a retry).
func repairPrompt(errorFindings []themecheck.Finding) string {
	var b strings.Builder
	b.WriteString("Your last proposal failed validation against the theme engine spec. Fix these specific problems " +
		"and resubmit the complete corrected set of files (not a diff):\n\n")
	for _, f := range errorFindings {
		if f.Path != "" {
			fmt.Fprintf(&b, "- [%s] %s: %s\n", f.Rule, f.Path, f.Message)
		} else {
			fmt.Fprintf(&b, "- [%s] %s\n", f.Rule, f.Message)
		}
	}
	// A rejection on an existing file is often a sign the resubmitted
	// content was reconstructed from memory rather than the real file —
	// e.g. dropping the mandatory layout-start/layout-end boilerplate when
	// regenerating a page you were only asked to make a small change to.
	// The tool loop is still available on this retry; use it.
	b.WriteString("\nIf you're unsure of a file's exact current content, call read_theme_file on it again " +
		"before resubmitting — don't reconstruct it from memory, that's how boilerplate like the layout " +
		"renders above gets silently dropped.")
	// A themecheck rejection is exactly the case action "edit" is for: the
	// findings above already say precisely which line(s) are wrong, so a
	// targeted old_string/new_string fix is normally both correct and far
	// smaller than resubmitting the whole file — see the intro sentence
	// above, which still applies (edit's server-side materialization always
	// produces that same complete, corrected file; it's just a cheaper way
	// to submit it, not a partial one).
	b.WriteString("\n\nFor most of these, action \"edit\" on the file you already have (a precise old_string/" +
		"new_string pair per finding) is the right fix — resubmit the whole file as action \"update\" only if the " +
		"correction is broad enough that a full rewrite is genuinely simpler.")
	return b.String()
}

// appendWarningsNote appends a short, merchant-readable note listing any
// warning-severity findings to summary. Rides on the existing summary text
// rather than a new chat_messages column — see phase 1 wiring notes; phase
// 3's generation_events log is the intended home for this once it exists.
func appendWarningsNote(summary string, warnings []themecheck.Finding) string {
	if len(warnings) == 0 {
		return summary
	}
	var b strings.Builder
	b.WriteString(summary)
	fmt.Fprintf(&b, "\n\nNote: %d warning(s):", len(warnings))
	for _, f := range warnings {
		if f.Path != "" {
			fmt.Fprintf(&b, "\n- %s: %s", f.Path, f.Message)
		} else {
			fmt.Fprintf(&b, "\n- %s", f.Message)
		}
	}
	return b.String()
}

// validateProposal re-checks every path the model proposed against the same
// rules internal/ai's system prompt already asked it to follow — defense in
// depth against a model mistake, never trusting model output as
// automatically safe just because it was asked nicely. mode restricts what's
// allowed further — see validateBrandModeProposal.
func validateProposal(r *ai.Result, mode string) error {
	if mode == ai.GenerationModeBrand {
		return validateBrandModeProposal(r)
	}
	for _, f := range r.Files {
		// Not re-embedding f.Path here: themefs' error already includes a
		// bounded preview of it. A proposal gone badly wrong can put an
		// entire file's content where a path belongs, and doubling that
		// blob into an outer wrapper is exactly the duplication that made
		// an earlier version of this error unreadable (and huge) in the
		// chat UI.
		if err := themefs.ValidateGeneratedFilePath(f.Path); err != nil {
			return fmt.Errorf("proposed file rejected: %w", err)
		}
		if f.Action != "create" && f.Action != "update" {
			return fmt.Errorf("file %q: invalid action %q", f.Path, f.Action)
		}
		// layout-start.liquid/layout-end.liquid may only ever be touched via
		// layout_links_to_add/layout_scripts_to_add (see buildWritePlan) —
		// never as a regular files[] entry. Nothing stopped the model from
		// doing both to the same file in one turn (observed in production:
		// a files[] edit to layout-start.liquid alongside a layout_links_to_add
		// entry), and buildWritePlan doesn't dedupe between the two paths —
		// planToStaged then produces two audit rows for the identical
		// (message_id, file_path) pair, and the second INSERT dies on
		// chat_generated_files' own uniqueness constraint. That failure
		// happens well after this point (post-themecheck, post-repair),
		// aborts persistFileRecords' whole batch, and surfaces to the
		// merchant as an opaque "something went wrong" with no indication
		// anything was even wrong with the proposal itself. Rejecting here
		// instead — before a single themecheck/repair round-trip is spent —
		// is cheap and gives the model something concrete to correct.
		if f.Path == pathLayoutStart || f.Path == pathLayoutEnd {
			return fmt.Errorf("proposed file rejected: %q may only be edited via layout_links_to_add/layout_scripts_to_add, not files[]", f.Path)
		}
	}
	for _, p := range r.LayoutLinksToAdd {
		if err := themefs.ValidateGeneratedFilePath(p); err != nil {
			return fmt.Errorf("proposed layout css link rejected: %w", err)
		}
	}
	for _, p := range r.LayoutScriptsToAdd {
		if err := themefs.ValidateGeneratedFilePath(p); err != nil {
			return fmt.Errorf("proposed layout js link rejected: %w", err)
		}
	}
	return nil
}

// validateBrandModeProposal enforces phase 7's brand-turn restriction: this
// turn may only ever update defaults.json — never a .liquid/.css/.js file,
// a page registration, or a layout link/script, all of which are
// structural decisions the brand turn was never asked to make.
func validateBrandModeProposal(r *ai.Result) error {
	for _, f := range r.Files {
		if f.Path != pathDefaultsJSON {
			return fmt.Errorf("brand mode may only propose %q, got %q", pathDefaultsJSON, f.Path)
		}
		if f.Action != "update" {
			return fmt.Errorf("brand mode: %q action must be \"update\", got %q", pathDefaultsJSON, f.Action)
		}
	}
	if r.PageRegistryEntry != nil {
		return fmt.Errorf("brand mode must not register a page")
	}
	if len(r.LayoutLinksToAdd) > 0 || len(r.LayoutScriptsToAdd) > 0 {
		return fmt.Errorf("brand mode must not register layout links/scripts")
	}
	return nil
}

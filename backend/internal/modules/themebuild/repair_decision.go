package themebuild

import (
	"context"
	"errors"
	"strings"

	"ai-chat/internal/ai"
	"ai-chat/internal/themecheck"
)

// RepairSkipReason is a safe, content-free label for why an AI repair
// Generate was not started. Never includes finding messages or file bodies.
type RepairSkipReason string

const (
	RepairSkipNone             RepairSkipReason = ""
	RepairSkipNoErrors         RepairSkipReason = "no_error_findings"
	RepairSkipBudgetExhausted  RepairSkipReason = "repair_budget_exhausted"
	RepairSkipContextCanceled  RepairSkipReason = "context_canceled"
	RepairSkipDeadlineExceeded RepairSkipReason = "deadline_exceeded"
)

// RepairReasonCategory is a coarse, safe classification of why repair ran.
// Values are rule-family tags only — never finding messages or file content.
type RepairReasonCategory string

const (
	RepairReasonNone            RepairReasonCategory = ""
	RepairReasonThemecheckError RepairReasonCategory = "themecheck_error"
	RepairReasonSchemaError     RepairReasonCategory = "schema_error"
	RepairReasonRequiredFile    RepairReasonCategory = "required_file_error"
	RepairReasonValidationError RepairReasonCategory = "validation_error"
)

// shouldStartRepairGenerate decides whether checkAndRepair may call Generate
// for an AI repair. errorCount is post-autofix / post-downgrade error findings.
// attempt is the current checkAndRepair loop counter (1-based); maxRetries is
// maxThemeCheckRetries (AI repair is refused once attempt > maxRetries).
func shouldStartRepairGenerate(ctx context.Context, attempt, maxRetries, errorCount int) (ok bool, reason RepairSkipReason) {
	if errorCount <= 0 {
		return false, RepairSkipNoErrors
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return false, RepairSkipDeadlineExceeded
		}
		return false, RepairSkipContextCanceled
	}
	if attempt > maxRetries {
		return false, RepairSkipBudgetExhausted
	}
	return true, RepairSkipNone
}

// prepareRepairThemeContext copies tc for a themecheck repair Generate and
// strips Phase-1 full-page / complex overrides so repair uses Repair budgets
// (token + iteration + tools), not the initial generation's ceilings.
func prepareRepairThemeContext(tc ai.ThemeContext) ai.ThemeContext {
	out := tc
	out.Repair = true
	out.EffortOverride = "medium"
	out.SimpleEditOneShot = false
	out.SimpleEditAllowRead = false
	out.PageCreatePrepared = false
	out.PageCreateAllowRead = false
	out.FullHomeRedesign = false
	out.DisableExplorationBrake = false
	// Clear overrides so resolveMaxTokens / toolIterationBudget use Repair=.
	out.MaxTokensOverride = 0
	out.MaxToolIterations = 0
	out.MaxExplorationToolCalls = 0
	out.MaxExplorationOnlyStreak = 0
	out.StreamMaxAttemptsOverride = 0
	out.FirstTokenTimeoutOverride = 0
	out.StreamIdleTimeoutOverride = 0
	// Focused context: recap + findings carry affected files; drop tree/manifest.
	out.FileTree = nil
	out.Manifest = nil
	return out
}

// repairReasonCategory maps themecheck findings to a safe log category.
// Prefers the most specific family among error findings; never includes messages.
func repairReasonCategory(findings []themecheck.Finding) RepairReasonCategory {
	if len(findings) == 0 {
		return RepairReasonNone
	}
	hasSchema := false
	hasRequired := false
	for _, f := range findings {
		if f.Severity != themecheck.SeverityError {
			continue
		}
		switch f.Rule {
		case "allowed-syntax", "known-fields", "balanced-tags", "bool-guard", "js-shape", "no-framework":
			hasSchema = true
		case "page-boilerplate", "asset-registered", "render-target-exists", "page-route":
			hasRequired = true
		}
	}
	if hasSchema {
		return RepairReasonSchemaError
	}
	if hasRequired {
		return RepairReasonRequiredFile
	}
	// theme-token, placeholder-body, page-requires-auth, etc.
	for _, f := range findings {
		if f.Severity == themecheck.SeverityError && strings.TrimSpace(f.Rule) != "" {
			return RepairReasonThemecheckError
		}
	}
	return RepairReasonValidationError
}

// maxRepairGenerateCalls is how many AI repair Generate calls one
// checkAndRepair invocation can make (attempt 1..maxThemeCheckRetries).
func maxRepairGenerateCalls() int {
	return maxThemeCheckRetries
}

// maxInitialProposalGenerateCalls is generateValidProposal's Generate budget
// (first attempt + maxThemeCheckRetries retries).
func maxInitialProposalGenerateCalls() int {
	return maxThemeCheckRetries + 1
}

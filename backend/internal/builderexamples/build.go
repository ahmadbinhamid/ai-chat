package builderexamples

import (
	"strings"
	"time"
)

// Input is the non-sensitive capture assembled by themebuild after a generation.
type Input struct {
	TenantID     uint64
	ChatID       string
	GenerationID string
	Prompt       string // used for fingerprint (+ optional sanitize); never stored raw by default

	Intent      string
	Complexity  string
	Compound    bool
	Operations  []string
	Targets     []string
	Constraints []string

	Context ContextSnapshot

	GenerationMode         string
	DeepSeekUsed           bool
	LocalLMUsed            bool
	LocalLMSkipped         bool
	RefinementApplied      bool
	RefinementRejected     bool
	DeterministicOperation string
	ToolKinds              []string
	ToolCount              int
	RepairCount            int
	RetryCount             int
	GenerateCalls          int
	Model                  string
	Provider               string

	Cancelled             bool
	Err                   error
	HasChanges            bool
	NeedsClarification    bool
	RepairBudgetExhausted bool
	WallStatus            string // e.g. succeeded / failed / cancelled
	Partial               bool

	Performance PerformanceSnapshot

	Now time.Time
}

// BuildExample constructs a compact Example with outcome labels.
func BuildExample(in Input, storeSanitized bool) Example {
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	protected, prefs, rest := ExtractProtectedFields(in.Constraints)
	cat, success, partial, failed, trainingPositive := classify(in)

	ex := Example{
		TenantID:          in.TenantID,
		ChatID:            in.ChatID,
		GenerationID:      in.GenerationID,
		PromptFingerprint: Fingerprint(in.Prompt),
		Intent:            in.Intent,
		Complexity:        in.Complexity,
		Compound:          in.Compound,
		Operations:        append([]string(nil), in.Operations...),
		Targets:           append([]string(nil), in.Targets...),
		Constraints:       rest,
		ProtectedFields:   protected,
		Preferences:       prefs,
		Context:           in.Context,
		Execution: ExecutionSnapshot{
			GenerationMode:         in.GenerationMode,
			DeepSeekUsed:           in.DeepSeekUsed,
			LocalLMUsed:            in.LocalLMUsed,
			LocalLMSkipped:         in.LocalLMSkipped,
			RefinementApplied:      in.RefinementApplied,
			RefinementRejected:     in.RefinementRejected,
			DeterministicOperation: in.DeterministicOperation,
			ToolKinds:              compactToolKinds(in),
			ToolCount:              in.ToolCount,
			RepairCount:            in.RepairCount,
			RetryCount:             in.RetryCount,
			GenerateCalls:          in.GenerateCalls,
			Model:                  in.Model,
			Provider:               in.Provider,
		},
		Outcome: OutcomeSnapshot{
			ValidationPassed: trainingPositive || (success && !in.RepairBudgetExhausted),
			FinalStatus:      in.WallStatus,
			Success:          success,
			Partial:          partial,
			Failed:           failed,
			Category:         cat,
			TrainingPositive: trainingPositive,
		},
		Performance: in.Performance,
		CreatedAt:   now,
	}
	if storeSanitized {
		ex.PromptSanitized = SanitizePrompt(in.Prompt, 200)
	}
	return ex
}

func classify(in Input) (cat OutcomeCategory, success, partial, failed, trainingPositive bool) {
	if in.Cancelled {
		return OutcomeCancelled, false, false, true, false
	}
	if in.NeedsClarification || strings.EqualFold(in.Intent, "ambiguous") {
		// Clarification is not a generation failure for routing learning.
		return OutcomeAmbiguous, true, false, false, false
	}
	if in.Err != nil {
		msg := strings.ToLower(in.Err.Error())
		switch {
		case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline"):
			return OutcomeProviderTimeout, false, false, true, false
		case strings.Contains(msg, "themecheck") || strings.Contains(msg, "validation") || strings.Contains(msg, "consistency"):
			return OutcomeValidationFailure, false, false, true, false
		case strings.Contains(msg, "provider") || strings.Contains(msg, "deepseek") || strings.Contains(msg, "anthropic") || strings.Contains(msg, "status 5"):
			return OutcomeProviderError, false, false, true, false
		default:
			return OutcomeUnknownFailure, false, false, true, false
		}
	}
	if in.Partial {
		return OutcomePartialSuccess, false, true, false, false
	}
	if in.DeterministicOperation != "" && !in.DeepSeekUsed {
		if in.DeterministicOperation == "register_existing_page" {
			return OutcomeLocalOperationSuccess, true, false, false, true
		}
		return OutcomeDeterministicSuccess, true, false, false, true
	}
	if in.RepairBudgetExhausted {
		return OutcomeValidationFailure, false, false, true, false
	}
	// Clean success: no error, not cancelled, validation not exhausted.
	return OutcomeSuccess, true, false, false, true
}

func compactToolKinds(in Input) []string {
	if len(in.ToolKinds) > 0 {
		return append([]string(nil), in.ToolKinds...)
	}
	if in.DeterministicOperation == "register_existing_page" {
		return []string{"discover_page", "read_pages_registry", "merge_registry", "validate"}
	}
	return nil
}

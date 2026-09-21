package builderexamples

import "time"

// OutcomeCategory is a bounded label for training/evaluation.
type OutcomeCategory string

const (
	OutcomeSuccess               OutcomeCategory = "success"
	OutcomePartialSuccess        OutcomeCategory = "partial_success"
	OutcomeValidationFailure     OutcomeCategory = "validation_failure"
	OutcomeProviderTimeout       OutcomeCategory = "provider_timeout"
	OutcomeProviderError         OutcomeCategory = "provider_error"
	OutcomeCancelled             OutcomeCategory = "cancelled"
	OutcomeAmbiguous             OutcomeCategory = "ambiguous"
	OutcomeDeterministicSuccess  OutcomeCategory = "deterministic_success"
	OutcomeLocalOperationSuccess OutcomeCategory = "local_operation_success"
	OutcomeUnknownFailure        OutcomeCategory = "unknown_failure"
)

// Example is one compact training/evaluation record.
// JSON tags are stable for JSONL export.
type Example struct {
	ID                string `json:"id"`
	TenantID          uint64 `json:"tenant_id"`
	ChatID            string `json:"chat_id,omitempty"`
	GenerationID      string `json:"generation_id,omitempty"`
	PromptFingerprint string `json:"prompt_fingerprint"`
	// PromptSanitized is optional and off by default — never secrets/tokens.
	PromptSanitized string `json:"prompt_sanitized,omitempty"`

	Intent          string   `json:"intent,omitempty"`
	Complexity      string   `json:"complexity,omitempty"`
	Compound        bool     `json:"compound,omitempty"`
	Operations      []string `json:"operations,omitempty"`
	Targets         []string `json:"targets,omitempty"`
	Constraints     []string `json:"constraints,omitempty"`
	ProtectedFields []string `json:"protected_fields,omitempty"`
	Preferences     []string `json:"preferences,omitempty"`

	Context ContextSnapshot `json:"context"`

	Execution ExecutionSnapshot `json:"execution"`

	Outcome OutcomeSnapshot `json:"outcome"`

	Performance PerformanceSnapshot `json:"performance"`

	CreatedAt time.Time `json:"created_at"`
}

// ContextSnapshot records selected context categories only (paths, not bodies).
type ContextSnapshot struct {
	PrimaryFiles          []string `json:"primary_files,omitempty"`
	DependencyFiles       []string `json:"dependency_files,omitempty"`
	SchemaFiles           []string `json:"schema_files,omitempty"`
	ReferenceFiles        []string `json:"reference_files,omitempty"`
	PrimaryCount          int      `json:"primary_count"`
	DependencyCount       int      `json:"dependency_count"`
	SchemaCount           int      `json:"schema_count"`
	ReferenceCount        int      `json:"reference_count"`
	HistoryMessagesBefore int      `json:"history_messages_before"`
	HistoryMessagesAfter  int      `json:"history_messages_after"`
	ContextBytesBefore    int      `json:"context_bytes_before"`
	ContextBytesAfter     int      `json:"context_bytes_after"`
	DuplicatesRemoved     int      `json:"duplicates_removed"`
}

// ExecutionSnapshot is a compact tool/routing trace (kinds only).
type ExecutionSnapshot struct {
	GenerationMode         string   `json:"generation_mode,omitempty"`
	DeepSeekUsed           bool     `json:"deepseek_used"`
	LocalLMUsed            bool     `json:"local_lm_used"`
	LocalLMSkipped         bool     `json:"local_lm_skipped,omitempty"`
	RefinementApplied      bool     `json:"refinement_applied,omitempty"`
	RefinementRejected     bool     `json:"refinement_rejected,omitempty"`
	DeterministicOperation string   `json:"deterministic_operation,omitempty"`
	ToolKinds              []string `json:"tool_kinds,omitempty"`
	ToolCount              int      `json:"tool_count"`
	RepairCount            int      `json:"repair_count"`
	RetryCount             int      `json:"retry_count"`
	GenerateCalls          int      `json:"generate_calls"`
	Model                  string   `json:"model,omitempty"`
	Provider               string   `json:"provider,omitempty"`
}

// OutcomeSnapshot separates label quality from category.
type OutcomeSnapshot struct {
	ValidationPassed bool            `json:"validation_passed"`
	FinalStatus      string          `json:"final_status,omitempty"`
	Success          bool            `json:"success"`
	Partial          bool            `json:"partial"`
	Failed           bool            `json:"failed"`
	Category         OutcomeCategory `json:"category"`
	// TrainingPositive is true only for clean successes suitable as positive examples.
	TrainingPositive bool `json:"training_positive"`
}

// PerformanceSnapshot holds timing metadata only.
type PerformanceSnapshot struct {
	BuilderPlanMs     int64 `json:"builder_plan_ms,omitempty"`
	LocalLMMs         int64 `json:"local_lm_ms,omitempty"`
	ContextPlanMs     int64 `json:"context_plan_ms,omitempty"`
	TTFTMs            int64 `json:"ttft_ms,omitempty"`
	TTFTAvailable     bool  `json:"ttft_available"`
	TotalGenerationMs int64 `json:"total_generation_ms"`
	PreModelMs        int64 `json:"pre_model_ms,omitempty"`
	ModelElapsedMs    int64 `json:"model_elapsed_ms,omitempty"`
}

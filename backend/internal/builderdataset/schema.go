package builderdataset

// Quality labels for training vs evaluation use.
type Quality string

const (
	QualityPositiveSemantic Quality = "positive_semantic" // refinement target usable for training
	QualityPositiveRouting  Quality = "positive_routing"  // intent/ops success without refinement target
	QualityNegativeEval     Quality = "negative_eval"     // bounded failure for evaluation
	QualityAmbiguousEval    Quality = "ambiguous_eval"    // clarification / ambiguous
	QualityExcluded         Quality = "excluded"
)

// FailureCategory mirrors useful ML-9 outcomes (bounded).
type FailureCategory string

const (
	FailNone                   FailureCategory = ""
	FailProviderTimeout        FailureCategory = "provider_timeout"
	FailValidationFailure      FailureCategory = "validation_failure"
	FailInvalidRefinement      FailureCategory = "invalid_refinement"
	FailAmbiguousRequest       FailureCategory = "ambiguous_request"
	FailUnsafeOperation        FailureCategory = "unsafe_operation"
	FailDeterministicOpFailure FailureCategory = "deterministic_operation_failure"
	FailCancelled              FailureCategory = "cancelled"
	FailProviderError          FailureCategory = "provider_error"
	FailPartialSuccess         FailureCategory = "partial_success"
	FailUnknown                FailureCategory = "unknown_failure"
)

// TrainingInput is the model-facing input (no production IDs).
type TrainingInput struct {
	Prompt              string   `json:"prompt,omitempty"`
	PromptFingerprint   string   `json:"prompt_fingerprint,omitempty"`
	Intent              string   `json:"intent"`
	Operations          []string `json:"operations"`
	ExistingConstraints []string `json:"existing_constraints,omitempty"`
}

// TrainingTarget is the semantic refinement / clarification target.
type TrainingTarget struct {
	Constraints        []string `json:"constraints,omitempty"`
	ProtectedFields    []string `json:"protected_fields,omitempty"`
	Preferences        []string `json:"preferences,omitempty"`
	NeedsClarification bool     `json:"needs_clarification"`
}

// TrainingMetadata is non-sensitive supporting context for analysis.
type TrainingMetadata struct {
	Complexity        string   `json:"complexity,omitempty"`
	Compound          bool     `json:"compound,omitempty"`
	ContextCategories []string `json:"context_categories,omitempty"` // primary|dependency|schema|reference
	Success           bool     `json:"success"`
	Source            string   `json:"source"` // deepseek|local_lm|deterministic|ambiguous|failure
	FailureCategory   string   `json:"failure_category,omitempty"`
	LocalLMUsed          bool   `json:"local_lm_used,omitempty"`
	LocalLMSkipped       bool   `json:"local_lm_skipped,omitempty"`
	RefinementApplied    bool   `json:"refinement_applied,omitempty"`
	RefinementRejected   bool   `json:"refinement_rejected,omitempty"`
	DeepSeekUsed         bool   `json:"deepseek_used,omitempty"`
	DeterministicOp      string `json:"deterministic_operation,omitempty"`
}

// TrainingExample is one sanitized training/eval row (JSONL line).
type TrainingExample struct {
	DatasetVersion string           `json:"dataset_version"`
	Split          string           `json:"split,omitempty"` // train|validation|test
	Input          TrainingInput    `json:"input"`
	Target         TrainingTarget   `json:"target"`
	Metadata       TrainingMetadata `json:"metadata"`
	Label          Quality          `json:"label"`
	SemanticKey    string           `json:"semantic_key,omitempty"` // for leakage checks; not a production ID
}

package providers

import "context"

// SemanticRefinement is the ONLY structured output the local 0.5B model may
// produce. It never invents intents, operations, file paths, or counts.
type SemanticRefinement struct {
	Constraints        []string `json:"constraints,omitempty"`
	ProtectedFields    []string `json:"protected_fields,omitempty"`
	Preferences        []string `json:"preferences,omitempty"`
	Clarification      string   `json:"clarification,omitempty"`
	NeedsClarification bool     `json:"needs_clarification,omitempty"`
	Confidence         float64  `json:"confidence,omitempty"`
	Source             string   `json:"source,omitempty"`
}

// MiniPlanHint is the tiny deterministic summary sent to the local model.
type MiniPlanHint struct {
	Intent     string   `json:"intent"`
	Operations []string `json:"operations"`
}

// Input is the minimal provider payload for semantic extraction.
type Input struct {
	Prompt string       `json:"prompt"`
	Plan   MiniPlanHint `json:"plan"`
}

// Provider extracts SemanticRefinement only — never invents operations/paths.
type Provider interface {
	Name() string
	Extract(ctx context.Context, in Input) (SemanticRefinement, error)
}

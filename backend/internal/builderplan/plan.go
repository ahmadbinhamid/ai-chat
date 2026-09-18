// Package builderplan is the local intelligence contract for AI Builder:
// fast prompt understanding → structured BuilderPlan → validation →
// minimal context selection. Pure CPU logic only — no DB, network, or disk.
//
// DeepSeek / existing generation remains responsible for complex language
// and content reasoning. This package never mutates theme files.
package builderplan

// IntentKind is the top-level classification of a merchant prompt.
type IntentKind string

const (
	IntentSimpleEdit         IntentKind = "simple_edit"
	IntentSectionEdit        IntentKind = "section_edit"
	IntentFullPage           IntentKind = "full_page"
	IntentCompound           IntentKind = "compound"
	IntentSEOMeta            IntentKind = "seo_meta"
	IntentPageCreate         IntentKind = "page_create"
	IntentNavigationRegistry IntentKind = "navigation_registry"
	IntentAmbiguous          IntentKind = "ambiguous"
)

// Complexity estimates planning cost before expensive generation.
type Complexity string

const (
	ComplexityLow    Complexity = "low"
	ComplexityMedium Complexity = "medium"
	ComplexityHigh   Complexity = "high"
)

// OperationKind is one atomic unit the builder pipeline can execute.
type OperationKind string

const (
	OpSimpleStyleEdit   OperationKind = "simple_style_edit"
	OpSectionEdit       OperationKind = "section_edit"
	OpFullPageEdit      OperationKind = "full_page_edit"
	OpCreatePage        OperationKind = "create_page"
	OpRegisterPage      OperationKind = "register_page"
	OpUpdateSEOMeta     OperationKind = "update_seo_meta"
	OpUpdatePageContent OperationKind = "update_page_content"
	OpAddToNavigation   OperationKind = "add_to_navigation"
	OpClarify           OperationKind = "clarify"
)

// Operation is one validated, non-destructive work item.
type Operation struct {
	Kind            OperationKind `json:"kind"`
	Target          string        `json:"target,omitempty"` // logical target (e.g. pages/blog.liquid)
	Label           string        `json:"label,omitempty"`
	Count           int           `json:"count,omitempty"` // e.g. N pages to create
	ProtectExisting bool          `json:"protect_existing"`
}

// BuilderPlan is the execution-ready contract produced before DeepSeek runs.
// It contains only fields needed by the existing Builder architecture.
type BuilderPlan struct {
	OriginalPrompt     string         `json:"original_prompt"`
	Intent             IntentKind     `json:"intent"`
	Complexity         Complexity     `json:"complexity"`
	Operations         []Operation    `json:"operations"`
	Targets            []string       `json:"targets"`
	RequiredFiles      []string       `json:"required_files"`
	Constraints        []string       `json:"constraints"`
	AcceptanceCriteria []string       `json:"acceptance_criteria"`
	Compound           bool           `json:"compound"`
	Ambiguous          bool           `json:"ambiguous"`
	Confidence         float64        `json:"confidence"` // 0–1; deterministic path uses 1 or low for ambiguous
	ClassifierSource   string         `json:"classifier_source"` // "deterministic" | future "local_ml"
}

// Classifier is the pluggable prompt-understanding surface.
// A small local ML/LM classifier can implement this later without changing
// the BuilderPlan contract. Implementations must stay pure (no I/O).
type Classifier interface {
	Classify(prompt string) Classification
}

// Classification is the raw intent signal before plan assembly.
type Classification struct {
	Intent     IntentKind
	Confidence float64
	Source     string
	Signals    []string // debug/routing hints only; never file bodies
}

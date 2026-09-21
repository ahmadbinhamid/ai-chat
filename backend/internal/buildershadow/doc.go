// Package buildershadow runs a candidate semantic-refinement model in SHADOW
// mode alongside the production BuilderPlan path.
//
// Production plan / DeepSeek / deterministic ops are never modified by shadow
// output. Candidate refinements are validated, compared, logged, then discarded.
//
// Default: disabled. There is currently no trained candidate — enabling shadow
// without a configured candidate provider is a no-op.
//
// Dependency direction: themebuild → buildershadow → builderintelligence / builderplan
package buildershadow

const (
	// StatusDiscarded is always the outcome for the user-facing plan.
	StatusDiscarded = "discarded_shadow_only"
)

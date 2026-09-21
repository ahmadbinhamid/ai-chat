package builderintelligence

import "ai-chat/internal/builderplan"

// Input is the minimal semantic payload for local understanding.
// Never include full theme dumps, pages.json bodies, or long chat histories.
type Input struct {
	Prompt            string
	DeterministicPlan builderplan.BuilderPlan
}

// Result is the outcome of Service.Understand — always safe to continue with.
type Result struct {
	Plan builderplan.BuilderPlan
	Meta Meta
}

// Meta is observability for the local understanding hop.
type Meta struct {
	Called                  bool
	Skipped                 bool
	Success                 bool
	Failure                 bool
	Timeout                 bool
	Fallback                bool
	Reason                  string
	Provider                string
	Model                   string
	ElapsedMs               int64
	InputBytes              int
	OutputBytes             int
	DeterministicConfidence float64
	RefinedConfidence       float64
	PlanChanged             bool
	OperationCountBefore    int
	OperationCountAfter     int
	RefinementApplied       bool
	RefinementRejected      bool
	RefinementFieldsCount   int
	PlanIntentBefore        string
	PlanIntentAfter         string
	PlanOpCountBefore       int
	PlanOpCountAfter        int
}

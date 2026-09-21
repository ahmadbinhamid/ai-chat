package builderoperations

import (
	"context"

	"ai-chat/internal/builderplan"
	"ai-chat/internal/themefs"
)

// Outcome is the user-safe result class of a deterministic operation.
type Outcome string

const (
	OutcomeSuccess       Outcome = "success"
	OutcomeAlreadyDone   Outcome = "already_done"
	OutcomeNotFound      Outcome = "not_found"
	OutcomeAmbiguous     Outcome = "ambiguous"
	OutcomeNoChange      Outcome = "no_change"
	OutcomeFailed        Outcome = "failed"
	OutcomeNotApplicable Outcome = "not_applicable"
)

// FileChange is one staged theme file mutation (never applied here).
type FileChange struct {
	Path    string
	Action  string
	Content string
}

// Metrics are structured observability fields (no prompt contents).
type Metrics struct {
	Called        bool
	Name          string
	Success       bool
	AlreadyDone   bool
	NotFound      bool
	Ambiguous     bool
	ElapsedMs     int64
	DeepSeekCalls int // always 0 on the local path
}

// Result is the operation outcome for themebuild orchestration.
type Result struct {
	Outcome            Outcome
	UserMessage        string
	NeedsClarification bool
	Files              []FileChange
	PageEntry          *themefs.PageEntry
	Diagnosis          *Diagnosis
	Metrics            Metrics
}

// HasChanges reports whether Apply/staging should run.
func (r Result) HasChanges() bool {
	return r.Outcome == OutcomeSuccess && len(r.Files) > 0
}

// Input is the validated contract for deterministic execution.
// Prompt may be used only for page identity discovery; never as free-form
// model text to rewrite pages.json.
type Input struct {
	Prompt string
	Plan   *builderplan.BuilderPlan // optional; when present, must already be validated
	Store  themefs.ThemeStore
	Auth   themefs.RequestAuth
}

// Operation is a narrow deterministic executor.
type Operation interface {
	Name() string
	Execute(ctx context.Context, in Input) (Result, error)
}

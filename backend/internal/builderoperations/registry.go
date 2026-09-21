package builderoperations

import (
	"context"
	"fmt"
	"sync"

	"ai-chat/internal/builderplan"
)

// Well-known operation names (extension points for future ops).
const (
	NameRegisterExistingPage = "register_existing_page"
	NameDiagnoseExistingPage = "diagnose_existing_page"
)

var (
	registryMu sync.RWMutex
	registry   = map[string]Operation{
		NameRegisterExistingPage: RegisterExistingPage{},
		NameDiagnoseExistingPage: DiagnoseExistingPage{},
	}
)

// Register adds or replaces an operation. Used by tests and future ops.
func Register(op Operation) {
	if op == nil {
		return
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[op.Name()] = op
}

// Lookup returns a registered operation by name.
func Lookup(name string) (Operation, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	op, ok := registry[name]
	return op, ok
}

// Resolve picks a deterministic operation from a validated BuilderPlan and/or
// prompt. ok=false means fall through to the existing DeepSeek workflow.
func Resolve(prompt string, plan *builderplan.BuilderPlan) (name string, ok bool) {
	if plan != nil && IsDeterministicPlan(*plan) {
		if n, found := operationNameFromPlan(*plan); found {
			return n, true
		}
	}
	if MatchesRegisterExistingPrompt(prompt) {
		return NameRegisterExistingPage, true
	}
	if MatchesTroubleshootPrompt(prompt) {
		return NameDiagnoseExistingPage, true
	}
	return "", false
}

// IsDeterministicPlan is true when the plan is eligible for local execution
// without DeepSeek / local ML / repair / retry.
func IsDeterministicPlan(plan builderplan.BuilderPlan) bool {
	if plan.Ambiguous || plan.Intent == builderplan.IntentAmbiguous {
		return false
	}
	_, ok := operationNameFromPlan(plan)
	return ok
}

func operationNameFromPlan(plan builderplan.BuilderPlan) (string, bool) {
	for _, op := range plan.Operations {
		switch op.Kind {
		case builderplan.OpRegisterExistingPage:
			return NameRegisterExistingPage, true
		case builderplan.OpDiagnoseExistingPage, builderplan.OpFixExistingPage:
			return NameDiagnoseExistingPage, true
		case builderplan.OpRegisterPage:
			// Legacy plan kind for register-existing under navigation_registry.
			if plan.Intent == builderplan.IntentNavigationRegistry && len(plan.Operations) == 1 {
				return NameRegisterExistingPage, true
			}
		}
	}
	if plan.Intent == builderplan.IntentPageTroubleshoot {
		return NameDiagnoseExistingPage, true
	}
	return "", false
}

// Run executes a named deterministic operation.
func Run(ctx context.Context, name string, in Input) (Result, error) {
	op, ok := Lookup(name)
	if !ok {
		return Result{}, fmt.Errorf("builderoperations: unknown operation %q", name)
	}
	return op.Execute(ctx, in)
}

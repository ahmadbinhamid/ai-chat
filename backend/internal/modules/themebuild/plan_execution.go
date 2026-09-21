package themebuild

import (
	"fmt"
	"log/slog"
	"strings"

	"ai-chat/internal/ai"
	"ai-chat/internal/builderplan"
)

// shouldShortCircuitClarification is true when BuilderPlan says the merchant
// must clarify before any generation. Callers must not load draft, sync
// workspace, call DeepSeek, or mutate files.
func shouldShortCircuitClarification(obs planObservation) bool {
	if !obs.Valid {
		return false
	}
	p := obs.Plan
	if p.Ambiguous || p.Intent == builderplan.IntentAmbiguous {
		return true
	}
	if len(p.Operations) == 1 && p.Operations[0].Kind == builderplan.OpClarify {
		return true
	}
	return !obs.NeedsDeepSeek && len(p.Operations) == 1 && p.Operations[0].Kind == builderplan.OpClarify
}

// ambiguousClarificationReply is the merchant-facing clarification when
// BuilderPlan is ambiguous. Matches the existing conversation fast-path
// (plain assistant message, ApplyStatusNotApplicable) — no second protocol.
func ambiguousClarificationReply(prompt string) string {
	_ = prompt
	return "What would you like to improve — the content, design, layout, or a specific page?"
}

func primaryPlannedOperation(plan builderplan.BuilderPlan) string {
	if len(plan.Operations) == 0 {
		return ""
	}
	return string(plan.Operations[0].Kind)
}

// executedOperationsFromResult maps a proposal onto BuilderPlan operation kinds
// for the plan/execution invariant (no secrets — paths/actions only).
func executedOperationsFromResult(result *ai.Result) []string {
	if result == nil {
		return nil
	}
	out := make([]string, 0, len(result.Files))
	seen := map[string]bool{}
	add := func(op string) {
		if op == "" || seen[op] {
			return
		}
		seen[op] = true
		out = append(out, op)
	}
	for _, f := range result.Files {
		path := strings.ToLower(strings.TrimSpace(f.Path))
		act := strings.ToLower(strings.TrimSpace(f.Action))
		switch {
		case path == "defaults.json":
			add(string(builderplan.OpAddToNavigation))
		case path == "pages.json":
			if act == "create" {
				add(string(builderplan.OpRegisterPage))
			} else {
				add(string(builderplan.OpUpdateSEOMeta))
			}
		case strings.HasPrefix(path, "pages/") && strings.HasSuffix(path, ".liquid"):
			if act == "create" {
				add(string(builderplan.OpCreatePage))
			} else if result.PageRegistryEntry != nil {
				add(string(builderplan.OpFixExistingPage))
			} else {
				add(string(builderplan.OpUpdatePageContent))
			}
		case strings.Contains(path, "button") || strings.HasSuffix(path, ".css"):
			add(string(builderplan.OpSimpleStyleEdit))
		case strings.HasPrefix(path, "components/"):
			add(string(builderplan.OpSectionEdit))
		}
	}
	if result.PageRegistryEntry != nil {
		add(string(builderplan.OpRegisterPage))
	}
	return out
}

// checkPlanExecutionMatch enforces: planned create_page must not execute as a
// pure content update of an existing page (and vice versa for the dangerous
// direction). On mismatch returns a safe internal error — never expose plan
// details to the merchant.
func checkPlanExecutionMatch(obs planObservation, result *ai.Result) error {
	if !obs.Valid || result == nil {
		return nil
	}
	if result.NeedsClarification || result.AnsweredQuestion {
		return nil
	}
	if !proposalHasChanges(result) {
		return nil
	}
	planned := obs.Plan.Operations
	executed := executedOperationsFromResult(result)
	planHasCreate := false
	planHasContentUpdate := false
	for _, op := range planned {
		switch op.Kind {
		case builderplan.OpCreatePage:
			planHasCreate = true
		case builderplan.OpUpdatePageContent, builderplan.OpFullPageEdit:
			planHasContentUpdate = true
		}
	}
	execHasCreate := false
	execHasContentUpdate := false
	for _, op := range executed {
		switch op {
		case string(builderplan.OpCreatePage):
			execHasCreate = true
		case string(builderplan.OpUpdatePageContent), string(builderplan.OpFullPageEdit):
			execHasContentUpdate = true
		}
	}

	mismatch := false
	reason := ""
	// Dangerous: plan said create new page(s) but execution only updated
	// existing content (live E2E Test D failure mode).
	if planHasCreate && !execHasCreate && execHasContentUpdate {
		mismatch = true
		reason = "planned_create_executed_content_update"
	}
	// Dangerous reverse: plan said update existing content but execution
	// created new page liquid(s).
	if planHasContentUpdate && !planHasCreate && execHasCreate {
		mismatch = true
		reason = "planned_content_update_executed_create"
	}

	slog.Info("ai: plan execution compare",
		"planned_operation", primaryPlannedOperation(obs.Plan),
		"executed_operation", strings.Join(executed, ","),
		"plan_execution_mismatch", mismatch,
		"mismatch_reason", reason)

	if !mismatch {
		return nil
	}
	slog.Warn("ai: plan execution mismatch",
		"planned_operation", primaryPlannedOperation(obs.Plan),
		"executed_operation", strings.Join(executed, ","),
		"plan_execution_mismatch", true,
		"mismatch_reason", reason)
	return fmt.Errorf("proposal/tool contract mismatch: planned operation does not match executed changes")
}

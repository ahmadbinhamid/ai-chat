package builderplan

import (
	"fmt"
	"strings"
)

// Validate checks a BuilderPlan before it may reach expensive generation.
// Rejects empty, destructive, or internally inconsistent plans.
func Validate(plan BuilderPlan) error {
	if strings.TrimSpace(plan.OriginalPrompt) == "" && plan.Intent != IntentAmbiguous {
		return fmt.Errorf("builderplan: empty prompt")
	}
	if plan.Intent == "" {
		return fmt.Errorf("builderplan: missing intent")
	}
	if len(plan.Operations) == 0 {
		return fmt.Errorf("builderplan: no operations")
	}
	if plan.Compound && len(plan.Operations) < 2 && plan.Intent == IntentCompound {
		// Allow multi create ops already expanded; still require ≥2 ops for compound intent.
		return fmt.Errorf("builderplan: compound plan must have at least 2 operations")
	}

	for i, op := range plan.Operations {
		if op.Kind == "" {
			return fmt.Errorf("builderplan: operation %d missing kind", i)
		}
		if !op.ProtectExisting && op.Kind != OpClarify {
			return fmt.Errorf("builderplan: operation %d must protect existing pages", i)
		}
		if err := rejectDestructive(op); err != nil {
			return err
		}
	}

	for _, c := range plan.Constraints {
		if looksDestructiveConstraint(c) {
			return fmt.Errorf("builderplan: destructive constraint %q", c)
		}
	}
	return nil
}

func rejectDestructive(op Operation) error {
	label := strings.ToLower(op.Label + " " + op.Target + " " + string(op.Kind))
	forbidden := []string{
		"delete all pages",
		"wipe theme",
		"rewrite entire theme",
		"overwrite all files",
		"replace whole theme",
		"destroy",
		"rm -rf",
	}
	for _, f := range forbidden {
		if strings.Contains(label, f) {
			return fmt.Errorf("builderplan: destructive operation rejected: %q", op.Label)
		}
	}
	// Whole-file theme wipe targets are never allowed from the planner.
	if op.Target == "*" || op.Target == "**" || op.Target == "theme/*" {
		return fmt.Errorf("builderplan: whole-theme target rejected")
	}
	return nil
}

func looksDestructiveConstraint(c string) bool {
	low := strings.ToLower(c)
	return strings.Contains(low, "delete_all") ||
		strings.Contains(low, "wipe_theme") ||
		strings.Contains(low, "allow_whole_theme_rewrite")
}

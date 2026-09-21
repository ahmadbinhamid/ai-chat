package builderplan

import (
	"fmt"
	"strings"
)

// PlanRefinement is structured local-intelligence output. It is never executed
// directly — ApplyRefinement + Validate must succeed before it can replace a plan.
// Provider/runtime code lives in builderintelligence, not here.
type PlanRefinement struct {
	Intent             IntentKind  `json:"intent,omitempty"`
	Complexity         Complexity  `json:"complexity,omitempty"`
	Operations         []Operation `json:"operations,omitempty"`
	Targets            []string    `json:"targets,omitempty"`
	RequiredFiles      []string    `json:"required_files,omitempty"`
	Constraints        []string    `json:"constraints,omitempty"`
	AcceptanceCriteria []string    `json:"acceptance_criteria,omitempty"`
	Confidence         float64     `json:"confidence,omitempty"`
	NeedsClarification bool        `json:"needs_clarification,omitempty"`
	Source             string      `json:"source,omitempty"`
	Notes              string      `json:"notes,omitempty"` // debug only; never executed
}

// DefaultAllowedOpKinds is the closed set local understanding may emit.
// Must stay a subset of buildercontract implemented/partial ops
// (see buildercontract.AllLifecycleRules + contract_test.go).
func DefaultAllowedOpKinds() []OperationKind {
	return []OperationKind{
		OpSimpleStyleEdit, OpSectionEdit, OpFullPageEdit,
		OpCreatePage, OpRegisterPage, OpRegisterExistingPage, OpUpdateSEOMeta,
		OpUpdatePageContent, OpAddToNavigation, OpDiagnoseExistingPage, OpFixExistingPage, OpClarify,
	}
}

// DefaultAllowedTargets bounds logical targets the LM may propose.
func DefaultAllowedTargets() []string {
	return []string{
		"pages/*.liquid", "pages/blog.liquid", "pages/home.liquid",
		"pages.json", "defaults.json", "css/theme.css",
		"components/*.liquid", "components/button.liquid",
		"components/header.liquid", "components/footer.liquid",
		"components/hero-slider", "button/style", "page",
	}
}

// ApplyRefinement merges a refinement onto the deterministic plan and validates.
// On any safety failure it returns the original plan and the error.
func ApplyRefinement(base BuilderPlan, ref PlanRefinement) (BuilderPlan, error) {
	out := base
	if ref.NeedsClarification || ref.Intent == IntentAmbiguous {
		out.Intent = IntentAmbiguous
		out.Ambiguous = true
		out.Operations = []Operation{{Kind: OpClarify, Label: "Clarify request", ProtectExisting: true}}
		out.Confidence = ref.Confidence
		if out.Confidence <= 0 {
			out.Confidence = 0.3
		}
		out.ClassifierSource = refinementSource(ref)
		out.AcceptanceCriteria = []string{
			"require_clarification_before_generation",
			"existing_pages_remain_unless_explicitly_targeted",
			"no_destructive_whole_file_rewrite_instructions",
		}
		if err := Validate(out); err != nil {
			return base, err
		}
		out.RequiredFiles = SelectContextFiles(out)
		return out, nil
	}
	if ref.Intent != "" {
		out.Intent = ref.Intent
		out.Ambiguous = ref.Intent == IntentAmbiguous
	}
	if ref.Complexity != "" {
		out.Complexity = ref.Complexity
	}
	if len(ref.Operations) > 0 {
		ops := make([]Operation, 0, len(ref.Operations))
		for _, op := range ref.Operations {
			if err := validateRefinementOperation(op); err != nil {
				return base, err
			}
			op.ProtectExisting = true
			ops = append(ops, op)
		}
		out.Operations = ops
		out.Compound = len(ops) >= 2 || out.Intent == IntentCompound
	}
	if len(ref.Targets) > 0 {
		for _, t := range ref.Targets {
			if !isAllowedTarget(t) {
				return base, fmt.Errorf("builderplan: refinement target %q not allowed", t)
			}
		}
		out.Targets = append([]string(nil), ref.Targets...)
	} else {
		out.Targets = uniqueTargets(out.Operations)
	}
	if len(ref.RequiredFiles) > 0 {
		for _, f := range ref.RequiredFiles {
			if !isAllowedRequiredFile(f) {
				return base, fmt.Errorf("builderplan: refinement file %q not allowed", f)
			}
		}
		out.RequiredFiles = append([]string(nil), ref.RequiredFiles...)
	} else {
		out.RequiredFiles = SelectContextFiles(out)
	}
	if len(ref.Constraints) > 0 {
		out.Constraints = uniqueStrings(append(append([]string{}, base.Constraints...), ref.Constraints...))
	}
	if len(ref.AcceptanceCriteria) > 0 {
		out.AcceptanceCriteria = uniqueStrings(append(append([]string{}, base.AcceptanceCriteria...), ref.AcceptanceCriteria...))
	}
	if ref.Confidence > 0 {
		out.Confidence = ref.Confidence
	}
	out.ClassifierSource = refinementSource(ref)
	if err := Validate(out); err != nil {
		return base, err
	}
	return out, nil
}

func refinementSource(ref PlanRefinement) string {
	if strings.TrimSpace(ref.Source) != "" {
		return ref.Source
	}
	return "local_lm"
}

func validateRefinementOperation(op Operation) error {
	if op.Kind == "" {
		return fmt.Errorf("builderplan: refinement operation missing kind")
	}
	allowed := false
	for _, k := range DefaultAllowedOpKinds() {
		if op.Kind == k {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("builderplan: refinement operation kind %q not allowed", op.Kind)
	}
	if op.Target != "" && !isAllowedTarget(op.Target) {
		return fmt.Errorf("builderplan: refinement operation target %q not allowed", op.Target)
	}
	return rejectDestructive(op)
}

func isAllowedTarget(t string) bool {
	t = strings.TrimSpace(t)
	for _, a := range DefaultAllowedTargets() {
		if t == a {
			return true
		}
	}
	if strings.HasPrefix(t, "pages/") && strings.HasSuffix(t, ".liquid") &&
		!strings.Contains(t, "..") && !strings.HasPrefix(t, "pages/css/") {
		return true
	}
	return false
}

func isAllowedRequiredFile(f string) bool {
	f = strings.TrimSpace(f)
	if isAllowedTarget(f) {
		return true
	}
	switch f {
	case "pages/css/blog.css", "pages/css/home.css", "pages/css/header.css", "pages/css/footer.css":
		return true
	}
	return false
}

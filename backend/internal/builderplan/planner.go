package builderplan

import (
	"fmt"
	"strings"
)

// defaultClassifier is used when BuildPlan is called without an override.
var defaultClassifier Classifier = DeterministicClassifier{}

// BuildPlan turns a merchant prompt into a validated BuilderPlan using the
// default deterministic classifier. It never loads theme files or calls DeepSeek.
func BuildPlan(prompt string) (BuilderPlan, error) {
	return BuildPlanWith(defaultClassifier, prompt)
}

// BuildPlanWith allows injecting a future local ML classifier while keeping
// the same plan assembly + validation path.
func BuildPlanWith(c Classifier, prompt string) (BuilderPlan, error) {
	if c == nil {
		c = defaultClassifier
	}
	raw := strings.TrimSpace(prompt)
	p := normalizePrompt(raw)
	class := c.Classify(raw)

	plan := BuilderPlan{
		OriginalPrompt:   raw,
		Intent:           class.Intent,
		Ambiguous:        class.Intent == IntentAmbiguous,
		Confidence:       class.Confidence,
		ClassifierSource: class.Source,
		Constraints: []string{
			"never_rewrite_unrelated_pages",
			"never_delete_existing_pages_unless_explicit",
			"never_whole_theme_rewrite_unless_explicit",
			"prefer_page_registry_entry_over_pages_json_rewrite_for_new_pages",
			"planner_does_not_mutate_theme_files",
		},
	}

	switch class.Intent {
	case IntentCompound:
		plan.Compound = true
		plan.Complexity = ComplexityHigh
		plan.Operations = buildCompoundOps(p)
	case IntentPageCreate:
		plan.Complexity = ComplexityMedium
		plan.Operations = []Operation{{
			Kind: OpCreatePage, Target: "pages/*.liquid", Label: "Create page",
			Count: 1, ProtectExisting: true,
		}, {
			Kind: OpRegisterPage, Target: "pages.json", Label: "Register page",
			ProtectExisting: true,
		}}
	case IntentNavigationRegistry:
		plan.Complexity = ComplexityMedium
		if registerExistingRe.MatchString(p) {
			target := "pages.json"
			slug := "page"
			if strings.Contains(p, "blog") {
				slug = "blog"
				target = "pages/blog.liquid"
			}
			plan.Operations = []Operation{{
				Kind: OpRegisterPage, Target: target, Label: "Register existing " + slug,
				ProtectExisting: true,
			}}
		} else {
			plan.Operations = []Operation{{
				Kind: OpAddToNavigation, Target: "defaults.json", Label: "Update navigation",
				ProtectExisting: true,
			}}
		}
	case IntentSEOMeta:
		plan.Complexity = ComplexityLow
		plan.Operations = []Operation{{
			Kind: OpUpdateSEOMeta, Target: "pages.json", Label: "Update meta titles",
			ProtectExisting: true,
		}}
		if strings.Contains(p, "only") {
			plan.Constraints = append(plan.Constraints, "seo_titles_only_preserve_other_seo_fields")
		}
	case IntentFullPage:
		plan.Complexity = ComplexityHigh
		target := "pages/blog.liquid"
		if strings.Contains(p, "home") {
			target = "pages/home.liquid"
		}
		kind := OpUpdatePageContent
		if fullPageRe.MatchString(p) && !contentRewriteRe.MatchString(p) {
			kind = OpFullPageEdit
		}
		plan.Operations = []Operation{{
			Kind: kind, Target: target, Label: "Update page content",
			ProtectExisting: true,
		}}
		if seoMetaRe.MatchString(p) {
			plan.Compound = true
			plan.Operations = append(plan.Operations, Operation{
				Kind: OpUpdateSEOMeta, Target: "pages.json", Label: "Update meta titles",
				ProtectExisting: true,
			})
			if strings.Contains(p, "only") {
				plan.Constraints = append(plan.Constraints, "seo_titles_only_preserve_other_seo_fields")
			}
		}
	case IntentSectionEdit:
		plan.Complexity = ComplexityMedium
		target := sectionTarget(p)
		plan.Operations = []Operation{{
			Kind: OpSectionEdit, Target: target, Label: "Edit section",
			ProtectExisting: true,
		}}
	case IntentSimpleEdit:
		plan.Complexity = ComplexityLow
		target := "components/*.liquid"
		if buttonRe.MatchString(p) {
			target = "button/style"
		}
		plan.Operations = []Operation{{
			Kind: OpSimpleStyleEdit, Target: target, Label: "Simple style edit",
			ProtectExisting: true,
		}}
	default: // ambiguous
		plan.Complexity = ComplexityLow
		plan.Operations = []Operation{{
			Kind: OpClarify, Label: "Clarify request", ProtectExisting: true,
		}}
		plan.AcceptanceCriteria = []string{
			"require_clarification_before_generation",
			"existing_pages_remain_unless_explicitly_targeted",
			"no_destructive_whole_file_rewrite_instructions",
		}
	}

	plan.Targets = uniqueTargets(plan.Operations)
	plan.RequiredFiles = SelectContextFiles(plan)
	if strings.Contains(p, "only") && seoMetaRe.MatchString(p) {
		plan.Constraints = append(plan.Constraints, "seo_titles_only_preserve_other_seo_fields")
	}
	if len(plan.AcceptanceCriteria) == 0 {
		plan.AcceptanceCriteria = defaultAcceptance(plan)
	}

	if err := Validate(plan); err != nil {
		return BuilderPlan{}, err
	}
	return plan, nil
}

func buildCompoundOps(p string) []Operation {
	ops := make([]Operation, 0, 6)
	n := requestedPageCount(p)
	if n >= 2 || createPageRe.MatchString(p) {
		if n < 2 {
			n = 1
		}
		for i := 1; i <= n; i++ {
			ops = append(ops, Operation{
				Kind: OpCreatePage, Target: "pages/*.liquid",
				Label: fmt.Sprintf("Create page %d of %d", i, n),
				Count: 1, ProtectExisting: true,
			})
		}
		ops = append(ops, Operation{
			Kind: OpRegisterPage, Target: "pages.json", Label: "Register new pages",
			ProtectExisting: true,
		})
	}
	if navRe.MatchString(p) && (addNavRe.MatchString(p) || createPageRe.MatchString(p) || n >= 2) {
		ops = append(ops, Operation{
			Kind: OpAddToNavigation, Target: "defaults.json", Label: "Add pages to navigation",
			ProtectExisting: true,
		})
	}
	if (contentRewriteRe.MatchString(p) || (strings.Contains(p, "blog") && strings.Contains(p, " and ") && seoMetaRe.MatchString(p))) &&
		(strings.Contains(p, "blog") || strings.Contains(p, "software")) && n < 2 {
		ops = append(ops, Operation{
			Kind: OpUpdatePageContent, Target: "pages/blog.liquid", Label: "Update blog content",
			ProtectExisting: true,
		})
	}
	if seoMetaRe.MatchString(p) {
		ops = append(ops, Operation{
			Kind: OpUpdateSEOMeta, Target: "pages.json", Label: "Update meta titles",
			ProtectExisting: true,
		})
	}
	if len(ops) < 2 && n >= 2 {
		// multi-page create alone is still compound (multiple create ops)
		return ops
	}
	if len(ops) == 0 {
		return []Operation{{Kind: OpClarify, Label: "Clarify compound request", ProtectExisting: true}}
	}
	return ops
}

func sectionTarget(p string) string {
	switch {
	case strings.Contains(p, "footer"):
		return "components/footer.liquid"
	case strings.Contains(p, "header") || strings.Contains(p, "nav"):
		return "components/header.liquid"
	case strings.Contains(p, "slider") || strings.Contains(p, "carousel") || strings.Contains(p, "hero"):
		return "components/hero-slider"
	default:
		return "components/*.liquid"
	}
}

func uniqueTargets(ops []Operation) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(ops))
	for _, op := range ops {
		if op.Target == "" {
			continue
		}
		if _, ok := seen[op.Target]; ok {
			continue
		}
		seen[op.Target] = struct{}{}
		out = append(out, op.Target)
	}
	return out
}

func defaultAcceptance(plan BuilderPlan) []string {
	out := []string{
		"existing_pages_remain_unless_explicitly_targeted",
		"no_destructive_whole_file_rewrite_instructions",
	}
	if plan.Compound {
		out = append(out, "each_operation_is_atomic", "partial_success_preserves_completed_steps")
	}
	for _, op := range plan.Operations {
		switch op.Kind {
		case OpCreatePage:
			out = append(out, "new_page_uses_page_registry_entry")
		case OpRegisterPage, OpUpdateSEOMeta:
			out = append(out, "pages_json_merge_preserves_existing_entries")
		case OpAddToNavigation:
			out = append(out, "defaults_json_appends_menu_items")
		case OpSimpleStyleEdit:
			out = append(out, "scoped_style_change_only")
		}
	}
	return uniqueStrings(out)
}

func uniqueStrings(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

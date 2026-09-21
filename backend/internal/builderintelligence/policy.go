package builderintelligence

import (
	"strings"

	"ai-chat/internal/builderplan"
)

// Decision explains whether the local layer should run.
type Decision struct {
	Call   bool
	Reason string
}

// ShouldCall asks: does local semantic extraction add useful information?
// Compound/create/register alone are NOT reasons to call — the deterministic
// plan already owns those. Call only for NL constraints, preferences, or ambiguity.
func ShouldCall(plan builderplan.BuilderPlan) Decision {
	p := normalizePrompt(plan.OriginalPrompt)

	// Deterministic structural ops — never spend local LM budget.
	if plan.Intent == builderplan.IntentNavigationRegistry && plan.Confidence >= 0.85 {
		return Decision{Call: false, Reason: "deterministic_register"}
	}
	if isRegisterExistingPrompt(p) && !isCompoundCreatePrompt(p) {
		return Decision{Call: false, Reason: "deterministic_register"}
	}
	if plan.Intent == builderplan.IntentSimpleEdit && plan.Confidence >= 0.9 &&
		!hasSemanticExtractionValue(p) {
		return Decision{Call: false, Reason: "deterministic_simple_edit"}
	}
	if isMultiPageCreateOnly(p, plan) && !hasSemanticExtractionValue(p) {
		return Decision{Call: false, Reason: "deterministic_page_create"}
	}
	if plan.Intent == builderplan.IntentPageCreate && plan.Confidence >= 0.9 &&
		!hasSemanticExtractionValue(p) {
		return Decision{Call: false, Reason: "deterministic_page_create"}
	}

	if plan.Ambiguous || plan.Intent == builderplan.IntentAmbiguous || isVagueSitePrompt(p) {
		return Decision{Call: true, Reason: "ambiguous"}
	}
	if hasSemanticExtractionValue(p) {
		return Decision{Call: true, Reason: "semantic_extraction"}
	}
	if plan.Confidence > 0 && plan.Confidence < 0.85 {
		return Decision{Call: true, Reason: "low_confidence"}
	}
	return Decision{Call: false, Reason: "deterministic_sufficient"}
}

func normalizePrompt(prompt string) string {
	p := strings.ToLower(strings.TrimSpace(prompt))
	return strings.Join(strings.Fields(p), " ")
}

func isVagueSitePrompt(p string) bool {
	return strings.Contains(p, "make the site better") ||
		strings.Contains(p, "improve the site") ||
		strings.Contains(p, "make it nicer") ||
		(strings.Contains(p, "better") && strings.Contains(p, "site") &&
			!strings.Contains(p, "page") && !strings.Contains(p, "blog") &&
			!strings.Contains(p, "button") && !strings.Contains(p, "header"))
}

// hasSemanticExtractionValue is true when the prompt carries NL constraints /
// preferences that deterministic classification does not fully capture.
func hasSemanticExtractionValue(p string) bool {
	if strings.Contains(p, "jpro") {
		return true
	}
	if strings.Contains(p, "according to") || strings.Contains(p, "software house") ||
		strings.Contains(p, "software company") || strings.Contains(p, "saas") {
		return true
	}
	if strings.Contains(p, "professional") || strings.Contains(p, "modern") ||
		strings.Contains(p, "audience") || strings.Contains(p, "tone") ||
		strings.Contains(p, "style") && (strings.Contains(p, "keep") || strings.Contains(p, "more")) {
		return true
	}
	preserve := strings.Contains(p, "keep") || strings.Contains(p, "preserve") ||
		strings.Contains(p, "don't change") || strings.Contains(p, "do not change") ||
		strings.Contains(p, "dont change") || strings.Contains(p, "still not change") ||
		strings.Contains(p, "leave") && (strings.Contains(p, "alone") || strings.Contains(p, "unchanged"))
	field := strings.Contains(p, "meta") || strings.Contains(p, "seo") ||
		strings.Contains(p, "title") || strings.Contains(p, "slug") ||
		strings.Contains(p, "description") || strings.Contains(p, "pricing")
	if preserve && field {
		return true
	}
	if strings.Contains(p, "only change") || strings.Contains(p, "but leave") ||
		strings.Contains(p, "but don't") || strings.Contains(p, "but do not") {
		return true
	}
	return false
}

func isRegisterExistingPrompt(p string) bool {
	if strings.Contains(p, "register") &&
		(strings.Contains(p, "blog") || strings.Contains(p, "page") ||
			strings.Contains(p, "register it") || strings.Contains(p, "if not register")) {
		if strings.Contains(p, "create") || strings.Contains(p, "generate") {
			return false
		}
		return true
	}
	return false
}

func isCompoundCreatePrompt(p string) bool {
	return (strings.Contains(p, "create") || strings.Contains(p, "generate") || strings.Contains(p, "make")) &&
		(strings.Contains(p, "page") || strings.Contains(p, "blog"))
}

func isMultiPageCreateOnly(p string, plan builderplan.BuilderPlan) bool {
	if !plan.Compound && plan.Intent != builderplan.IntentCompound && plan.Intent != builderplan.IntentPageCreate {
		return false
	}
	// Pure create-N-pages with no preserve/audience language.
	return isCompoundCreatePrompt(p) && !hasSemanticExtractionValue(p)
}

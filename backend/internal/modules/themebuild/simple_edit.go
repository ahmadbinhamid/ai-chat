package themebuild

import (
	"strings"
	"unicode"
)

// isSimpleInteractiveEdit reports prompts that should take the local-first
// fast path: named target + change/redesign intent, no multi-page / brand /
// reference complexity. Used to tighten tool-iteration budgets and prefer a
// bounded propose_changes over long clarification loops.
func isSimpleInteractiveEdit(prompt, mode string) bool {
	if mode != "" && mode != "edit" {
		return false
	}
	p := strings.ToLower(strings.TrimSpace(prompt))
	if p == "" || len([]rune(p)) > 240 {
		return false
	}
	// Full section rebuilds belong on complex_page, not the 8k one-shot.
	if isSectionRedesignPrompt(p) || isSliderFeaturePrompt(p) || isPageCreateOrStructural(p) {
		return false
	}
	// Skip when the merchant attached complexity signals.
	for _, bad := range []string{
		"http://", "https://", "every page", "all pages", "whole site",
		"entire theme", "from scratch", "new theme", "brand kit",
	} {
		if strings.Contains(p, bad) {
			return false
		}
	}
	hasTarget := false
	for _, t := range []string{
		"header", "footer", "nav", "navbar", "menu", "hero", "button",
		"banner", "logo", "cart", "sidebar", "homepage", "home page",
	} {
		if strings.Contains(p, t) {
			hasTarget = true
			break
		}
	}
	if !hasTarget {
		return false
	}
	hasAction := false
	for _, a := range []string{
		"change", "update", "edit", "redesign", "restyle", "improve",
		"fix", "make", "tweak", "adjust", "modify",
	} {
		if strings.Contains(p, a) {
			hasAction = true
			break
		}
	}
	if !hasAction {
		return false
	}
	// Mostly letters/spaces — not a huge paste.
	letters := 0
	for _, r := range p {
		if unicode.IsLetter(r) || unicode.IsSpace(r) || r == '\'' || r == '"' || r == ',' || r == '.' || r == '!' || r == '?' {
			letters++
		}
	}
	return letters*10 >= len([]rune(p))*7
}

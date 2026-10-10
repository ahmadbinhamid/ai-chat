package pageintent

import "regexp"

// createPageRe needs an article before "page", so "make the page dark" (an edit) never reads as a new page.
var createPageRe = regexp.MustCompile(`(?i)\b(?:create|add|build|make|set\s+up|design)\s+(?:me\s+)?(?:a|an)\s+(?:new\s+)?(?:[\w'-]+\s+){0,3}page\b`)

// DetectCreatePage reports whether prompt asks for a new page.
func DetectCreatePage(prompt string) bool {
	return createPageRe.MatchString(prompt)
}

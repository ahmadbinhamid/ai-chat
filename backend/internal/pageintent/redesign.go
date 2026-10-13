package pageintent

import "regexp"

// redesignRe names a request for a visibly new design rather than an adjustment; "redesig" keeps a seen typo matching.
var redesignRe = regexp.MustCompile(`(?i)\b(?:` +
	`re-?desig(?:n|ned|ning|ns)?|revamp(?:ed|ing|s)?|make-?over|` +
	`(?:new|fresh|whole new|brand new)\s+look|` +
	`(?:modern|premium|stunning|professional|luxurious|luxury|sleek|beautiful|high-end)\s+(?:look|design|feel)|` +
	`look\s+(?:more\s+|really\s+|much\s+more\s+)?(?:modern|premium|stunning|professional|luxurious|sleek|high-end)|` +
	`re-?build(?:ing)?\s+(?:the\s+|my\s+|this\s+|our\s+)?(?:whole\s+|entire\s+)?(?:page|homepage|home\s?page|site|website|store|shop|theme)|` +
	`completely\s+(?:change|redo|rework|transform|restyle)|` +
	`(?:look|feel)\s+like\s+a\s+real\s+(?:brand|store|shop|business)` +
	`)\b`)

// referenceLookRe is "make it look like <x>": a redesign only when a reference (file, link or image) is attached.
var referenceLookRe = regexp.MustCompile(`(?i)\b(?:make|style|design)\s+(?:it|this|the\s+\w+(?:\s+page)?|my\s+\w+)\s+(?:look\s+)?like\b`)

// DetectRedesign reports whether prompt asks to redesign a page or the store rather than adjust part of it.
// hasReference is whether the turn carries a reference design (HTML file, link or image).
func DetectRedesign(prompt string, hasReference bool) bool {
	return redesignRe.MatchString(prompt) || (hasReference && referenceLookRe.MatchString(prompt))
}

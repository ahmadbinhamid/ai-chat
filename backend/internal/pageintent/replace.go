package pageintent

import "regexp"

// replaceRe names a request to replace something wholesale; earlier unsaved work in the files it rewrites is then
// meant to go, so the draft-reversion check lets it through with a note instead of sending it to repair.
var replaceRe = regexp.MustCompile(`(?i)\b(?:re-?desig(?:n|ned|ning)?|re-?build(?:ing)?|rebuilt|replac(?:e|ed|es|ing)|start(?:ing)? (?:it )?over|from scratch|completely)\b`)

// DetectReplace reports whether prompt asks to redesign, rebuild or replace something rather than adjust it.
func DetectReplace(prompt string) bool {
	return replaceRe.MatchString(prompt)
}

// Package pageintent recognizes narrow, explicit page-lifecycle requests (e.g. "register the
// pricing page") that themebuild.Service can answer deterministically; ambiguous prompts always resolve to "no match."
package pageintent

import (
	"regexp"
	"strings"
)

// maxPromptLen bounds detectors to short, single-purpose requests; a long prompt is
// presumptively asking for more than this narrow op covers.
const maxPromptLen = 200

// editCueRe matches phrasing meaning "I want content/design changed." Deliberately
// over-inclusive: a false positive here just falls back to normal generation.
var editCueRe = regexp.MustCompile(`(?i)\b(redesign|rewrite|re-?write|recreate|rebuild|new design|change the (?:design|look|content|color|colour|style|layout)|update the content|add a section|add a slider|remove the|delete the|make it|improve|redo|restyle|reword|fix|repair)\b`)

// registerVerbRe requires "register" to open the prompt (anchored), so "fix the register page"
// (where "register" is a real page name) is never mistaken for a register command.
var registerVerbRe = regexp.MustCompile(`(?i)^(?:please\s+|pls\s+|can you\s+|could you\s+|would you\s+)?register(?:ing|ed)?\b`)

// troubleshootPhraseRe covers ways a merchant reports a page as broken, never "make it better."
var troubleshootPhraseRe = regexp.MustCompile(`(?i)\b(not working|isn'?t working|doesn'?t work|not opening|isn'?t opening|won'?t open|can'?t open|cant open|not loading|won'?t load|wont load|is broken|stopped working|404|can'?t find|cant find|not showing up|not showing|nothing (?:happens|shows))\b`)

// pageWordRe locates the literal word "page"; extractPageName reads backward from it for the name.
var pageWordRe = regexp.MustCompile(`(?i)\bpage\b`)

// baseNameStopWords are filler words stripped from the name found before "page".
var baseNameStopWords = map[string]bool{
	"the": true, "my": true, "our": true, "this": true, "please": true, "pls": true,
}

// registerNameStopWords adds the register verb/subject words. Not reused by the diagnose
// detector, since "register" can legitimately be a real page name there.
var registerNameStopWords = mergedStopWords(map[string]bool{
	"register": true, "registering": true, "registered": true,
	"can": true, "could": true, "would": true, "you": true,
})

// diagnoseNameStopWords adds question words a troubleshooting report commonly opens with.
var diagnoseNameStopWords = mergedStopWords(map[string]bool{
	"why": true, "is": true, "are": true, "does": true, "do": true, "did": true,
	"what": true, "how": true, "where": true, "when": true,
})

func mergedStopWords(extra map[string]bool) map[string]bool {
	merged := make(map[string]bool, len(baseNameStopWords)+len(extra))
	for w := range baseNameStopWords {
		merged[w] = true
	}
	for w := range extra {
		merged[w] = true
	}
	return merged
}

// hasEditCue reports whether prompt signals a content/design change is
// wanted, not just a registration or status check.
func hasEditCue(prompt string) bool {
	return editCueRe.MatchString(prompt)
}

// DetectRegisterExisting reports whether prompt explicitly asks to register an existing page.
// name is the raw captured name; the caller must still confirm a matching file exists.
func DetectRegisterExisting(prompt string) (name string, ok bool) {
	return detect(prompt, registerVerbRe, registerNameStopWords)
}

// DetectDiagnoseExisting reports whether prompt explicitly reports a named page as broken.
// Same name/caller-verifies contract as DetectRegisterExisting.
func DetectDiagnoseExisting(prompt string) (name string, ok bool) {
	return detect(prompt, troubleshootPhraseRe, diagnoseNameStopWords)
}

// detect holds the checks both detectors share; triggerRe and stopWords differ per caller.
func detect(prompt string, triggerRe *regexp.Regexp, stopWords map[string]bool) (name string, ok bool) {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" || len(trimmed) > maxPromptLen {
		return "", false
	}
	if !triggerRe.MatchString(trimmed) {
		return "", false
	}
	if hasEditCue(trimmed) {
		return "", false
	}
	return extractPageName(trimmed, stopWords)
}

// extractPageName reads backward from "page", stripping stopWords, to recover just the name.
// Uses word-splitting, not a regex capture group: RE2 has no lookbehind to exclude the trigger verb.
func extractPageName(prompt string, stopWords map[string]bool) (string, bool) {
	loc := pageWordRe.FindStringIndex(prompt)
	if loc == nil {
		return "", false
	}
	before := strings.TrimSpace(prompt[:loc[0]])
	if before == "" {
		return "", false
	}
	words := strings.Fields(before)
	// Bounds how far back a long prompt's unrelated clauses can reach.
	if len(words) > 5 {
		words = words[len(words)-5:]
	}
	i := 0
	for i < len(words) && stopWords[strings.ToLower(strings.Trim(words[i], ".,!?"))] {
		i++
	}
	words = words[i:]
	if len(words) == 0 {
		return "", false
	}
	return strings.Join(words, " "), true
}

// Slugify turns a captured page name into a candidate kebab-case slug; never guarantees the slug is real.
func Slugify(name string) string {
	var b strings.Builder
	lastSep := true // starts true so a leading separator run emits nothing
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			lastSep = false
		default:
			if !lastSep {
				b.WriteByte('-')
				lastSep = true
			}
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

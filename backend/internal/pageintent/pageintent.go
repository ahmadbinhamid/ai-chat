// Package pageintent recognizes a small, deliberately narrow set of
// explicit page-lifecycle requests — "register the pricing page", "the
// contact page is not working" — that themebuild.Service can answer
// deterministically, without ever calling the model. Per CLAUDE.md rule 2,
// this package is pure prompt matching only: no DB, no network, no disk.
// themebuild.Service.tryDeterministicPageOp does the actual file lookup and
// (for register) the write that a positive match here only hints at.
//
// Every detector here is intentionally tight, not a general intent
// classifier. False positives are the real risk — a merchant asking for a
// redesign silently getting a registry edit or a status report instead of
// the change they actually wanted — so each detector requires an explicit
// trigger word/phrase AND rejects the prompt outright if it also contains
// any cue that the merchant wants content or design changed (see
// hasEditCue). A detector missing a real register/diagnose request just
// falls through to normal generation, which can still handle it at full
// cost — the safe failure mode. A detector wrongly firing is not, so every
// ambiguous case here resolves to "no match."
package pageintent

import (
	"regexp"
	"strings"
)

// maxPromptLen bounds every detector to a short, single-purpose request —
// the same reasoning themebuild's own simple-edit fast path uses a prompt-
// length guard for: a long, multi-part prompt is presumptively asking for
// more than this narrow op covers, even if it happens to also contain a
// trigger phrase somewhere in it.
const maxPromptLen = 200

// editCueRe matches phrasing that means "I want content or design
// changed," not just a registration or status check. Deliberately broad
// and over-inclusive: a false positive here (bailing on a prompt that
// actually was a clean register/diagnose request) just costs a fallback to
// normal generation, not a wrong action.
var editCueRe = regexp.MustCompile(`(?i)\b(redesign|rewrite|re-?write|recreate|rebuild|new design|change the (?:design|look|content|color|colour|style|layout)|update the content|add a section|add a slider|remove the|delete the|make it|improve|redo|restyle|reword|fix|repair)\b`)

// registerVerbRe requires "register" to open the prompt (optionally behind
// a short, fixed set of polite prefixes) — anchored, not just present
// anywhere, specifically so a prompt like "fix the register page" (where
// "register" is the name of the theme's own signup page — see
// themecheck's systemPageTypes) is never mistaken for a request to
// register some other page. A register REQUEST reads as a command and
// naturally opens the sentence; "register" appearing elsewhere is almost
// always the noun.
var registerVerbRe = regexp.MustCompile(`(?i)^(?:please\s+|pls\s+|can you\s+|could you\s+|would you\s+)?register(?:ing|ed)?\b`)

// troubleshootPhraseRe is the small, fixed set of ways a merchant reports a
// page as broken — deliberately scoped to phrasing that says "something is
// wrong," never "make it better."
var troubleshootPhraseRe = regexp.MustCompile(`(?i)\b(not working|isn'?t working|doesn'?t work|not opening|isn'?t opening|won'?t open|can'?t open|cant open|not loading|won'?t load|wont load|is broken|stopped working|404|can'?t find|cant find|not showing up|not showing|nothing (?:happens|shows))\b`)

// pageWordRe locates the literal word "page" — extractPageName reads
// backward from here for the actual name, since every phrasing this
// package targets puts the page name immediately before that word
// ("the pricing page", "our contact page").
var pageWordRe = regexp.MustCompile(`(?i)\bpage\b`)

// baseNameStopWords are filler words stripped from the front of whatever
// extractPageName finds before "page", common to both detectors.
var baseNameStopWords = map[string]bool{
	"the": true, "my": true, "our": true, "this": true, "please": true, "pls": true,
}

// registerNameStopWords adds the register command's own verb/subject words
// — deliberately does NOT reuse this for the diagnose detector, since
// "register" can legitimately be a page's real name there (the theme's own
// signup page — see registerVerbRe's doc comment for the same ambiguity
// from the other direction).
var registerNameStopWords = mergedStopWords(map[string]bool{
	"register": true, "registering": true, "registered": true,
	"can": true, "could": true, "would": true, "you": true,
})

// diagnoseNameStopWords adds the question words a troubleshooting report
// commonly opens with — "why is the X page not working."
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

// DetectRegisterExisting reports whether prompt is an explicit, narrow
// request to register an already-existing page into the theme's registry
// — "register the pricing page", "please register my about-us page". name
// is the raw (unslugified) captured page name; a match here is only a
// candidate — the caller must still confirm a matching file exists before
// acting (see Slugify and themebuild.Service.tryRegisterExistingPage). A
// prompt with no capturable name at all ("register the page") deliberately
// does not match — resolving "the page" to a specific file would need
// conversation context this package doesn't have.
func DetectRegisterExisting(prompt string) (name string, ok bool) {
	return detect(prompt, registerVerbRe, registerNameStopWords)
}

// DetectDiagnoseExisting reports whether prompt is an explicit, narrow
// report that an existing, named page is broken — "the pricing page is not
// working", "contact page won't open". Same name/caller-verifies contract
// as DetectRegisterExisting.
func DetectDiagnoseExisting(prompt string) (name string, ok bool) {
	return detect(prompt, troubleshootPhraseRe, diagnoseNameStopWords)
}

// detect holds the checks DetectRegisterExisting and DetectDiagnoseExisting
// share: length bound, triggerRe match, the edit-cue exclusion, and pulling
// a page name out via extractPageName. triggerRe and stopWords are the two
// things that differ between the two callers.
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

// extractPageName finds the literal word "page" in prompt and reads
// backward from it, stripping any leading filler word in stopWords, to
// recover just the real page name — "register the pricing page" with
// registerNameStopWords yields "pricing", not "register the pricing".
//
// A plain regex capture group can't do this safely: a single greedy group
// spanning multiple words happily swallows the trigger verb itself
// ("register the pricing") since RE2 has no lookahead/lookbehind to exclude
// it, and Go's regexp package is RE2-only (no backtracking engine to fall
// back on). Explicit word-splitting sidesteps that entirely.
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
	// At most the last 5 words immediately before "page" are ever a
	// plausible name — bounds how far back a long prompt's unrelated
	// clauses can reach.
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

// Slugify turns a captured page name into a candidate kebab-case slug —
// "Contact Us" -> "contact-us", "  Pricing  " -> "pricing". Purely
// mechanical; the caller still must confirm a real file exists at that
// slug (see DetectRegisterExisting's doc comment) before treating it as a
// match — this never guarantees the slug is real.
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

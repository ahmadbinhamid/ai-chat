package urlfetch

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// urlPattern matches an http(s) URL greedily up to the next whitespace.
// ExtractFirstURL then trims trailing prose punctuation (trailingPunctuation)
// that isn't actually part of the link.
var urlPattern = regexp.MustCompile(`https?://\S+`)

// trailingPunctuation is stripped repeatedly from a matched URL's end (for
// multiple trailing marks, e.g. "(https://example.com)."). Includes fullwidth
// CJK punctuation (。，）！？；：) alongside ASCII.
const trailingPunctuation = ".,;:!?)]}\"'。，）！？；："

// ExtractFirstURL returns the first http(s) URL in text — detects a merchant
// pasting a reference link directly in a prompt, rather than requiring a
// dedicated attach step. Only the first match is used, matching the existing
// one-reference-attachment-per-turn rule. Purely syntactic: ValidateURL
// decides whether the match is actually safe to fetch.
func ExtractFirstURL(text string) (string, bool) {
	match := urlPattern.FindString(text)
	// Trim by RUNE, not byte: a fullwidth punctuation mark is multi-byte in
	// UTF-8, so trimming its trailing byte alone would leave an invalid encoding.
	for len(match) > 0 {
		r, size := utf8.DecodeLastRuneInString(match)
		if r == utf8.RuneError || !strings.ContainsRune(trailingPunctuation, r) {
			break
		}
		match = match[:len(match)-size]
	}
	if match == "" {
		return "", false
	}
	return match, true
}

// referenceCuePhrases signal a merchant means a URL in their prompt as a
// reference to read/build from, not just an incidental mention (their own
// shop address, a support link). Matched anywhere in the prompt, not
// specifically "near" the URL — requiring adjacency would be fragile
// against ordinary word order.
var referenceCuePhrases = []string{
	"this link", "this site", "this page", "like this", "reference",
	"similar to", "check", "look at", "clone", "inspired by",
}

// referenceCuePattern is built once at package init (ExtractReferenceURL
// runs per prompt). \b enforces word boundaries — without it, "check" would
// match inside "checkout"/"checkbox"/"checked", ordinary storefront words
// unrelated to a reference link.
var referenceCuePattern = regexp.MustCompile(buildCuePattern(referenceCuePhrases))

func buildCuePattern(phrases []string) string {
	quoted := make([]string, len(phrases))
	for i, p := range phrases {
		quoted[i] = regexp.QuoteMeta(p)
	}
	return `(?i)\b(` + strings.Join(quoted, "|") + `)\b`
}

// referenceURLDominanceThreshold: how much of the prompt's non-whitespace
// text the URL itself must be to count as a reference with no cue phrase
// needed — a prompt that's essentially just a pasted link unambiguously means it.
const referenceURLDominanceThreshold = 0.6

// ExtractReferenceURL is ExtractFirstURL plus an intent check, so a URL
// merely mentioned in passing ("our shop is at https://example.com — make
// the header blue") isn't treated as a fetch-worthy reference. Returns a URL
// only when it dominates the prompt (referenceURLDominanceThreshold) or the
// prompt contains a referring cue (referenceCuePhrases); otherwise false.
//
// Stopgap: the durable fix is an explicit "add link" UI affordance, out of
// scope here (frontend change).
func ExtractReferenceURL(prompt string) (string, bool) {
	url, ok := ExtractFirstURL(prompt)
	if !ok {
		return "", false
	}

	promptChars := countNonSpace(prompt)
	if promptChars > 0 && float64(countNonSpace(url))/float64(promptChars) >= referenceURLDominanceThreshold {
		return url, true
	}

	// Cut the URL out before the cue check — its own host/path text
	// (https://example.com/check) could otherwise satisfy referenceCuePattern
	// on the link's own spelling rather than what the merchant actually wrote.
	promptWithoutURL := strings.Replace(prompt, url, "", 1)
	if referenceCuePattern.MatchString(promptWithoutURL) {
		return url, true
	}
	return "", false
}

func countNonSpace(s string) int {
	n := 0
	for _, r := range s {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}

package urlfetch

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// urlPattern matches an http(s) URL greedily up to the next whitespace.
// ExtractFirstURL then trims trailing prose punctuation (trailingPunctuation)
var urlPattern = regexp.MustCompile(`https?://\S+`)

// trailingPunctuation is stripped repeatedly from a matched URL's end (for
// multiple trailing marks, e.g. "(https://example.com)."). Includes fullwidth
const trailingPunctuation = ".,;:!?)]}\"'。，）！？；："

// ExtractFirstURL returns the first http(s) URL in text — detects a merchant
// pasting a reference link directly in a prompt, rather than requiring a
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
var referenceCuePhrases = []string{
	"this link", "this site", "this page", "like this", "reference",
	"similar to", "check", "look at", "clone", "inspired by",
}

// referenceCuePattern is built once at package init (ExtractReferenceURL
// runs per prompt). \b enforces word boundaries — without it, "check" would
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
const referenceURLDominanceThreshold = 0.6

// ExtractReferenceURL is ExtractFirstURL plus an intent check, so a URL
// merely mentioned in passing ("our shop is at https://example.com — make
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

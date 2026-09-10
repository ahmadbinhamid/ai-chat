package urlfetch

import (
	"regexp"
	"strings"
	"unicode"
)

// urlPattern matches an http(s) URL starting at a word boundary — greedy up
// to the next whitespace character, since a URL's own valid character set
// doesn't include whitespace. ExtractFirstURL trims trailing punctuation a
// human would naturally leave attached in prose (see trailingPunctuation)
// that isn't actually part of the link.
var urlPattern = regexp.MustCompile(`https?://\S+`)

// trailingPunctuation is stripped, one rune at a time, from the end of a
// matched URL — repeatedly, in case more than one trails (e.g. a URL at
// the end of a parenthetical sentence: "...like this (https://example.com).").
const trailingPunctuation = ".,;:!?)]}\"'"

// ExtractFirstURL returns the first http(s) URL found anywhere in text —
// used to detect a merchant pasting a reference link directly in their
// prompt ("https://example.com can you access this link"), rather than
// requiring a dedicated upload/attach step the way the image/HTML-file
// attachment features do. Only the first match is used, matching the
// existing "one reference attachment per turn" rule the HTML-file-upload
// feature already established (see themebuild.GenerateInput's
// HTMLAttachmentFilename doc comment) — a merchant pasting more than one
// link in the same message is treated the same way as attaching more than
// one HTML file: not supported, only the first one is used. Purely
// syntactic — ValidateURL (guard.go) is what actually decides whether the
// match is a safe, fetchable URL.
func ExtractFirstURL(text string) (string, bool) {
	match := urlPattern.FindString(text)
	for len(match) > 0 && strings.ContainsRune(trailingPunctuation, rune(match[len(match)-1])) {
		match = match[:len(match)-1]
	}
	if match == "" {
		return "", false
	}
	return match, true
}

// referenceCuePhrases are case-insensitive phrases that, appearing anywhere
// in a prompt alongside a URL, signal the merchant means that URL as a
// reference to read or build from — as opposed to a URL that's merely
// present for some other reason (their own shop's address, a support link,
// an aside in a longer message about something else). Matched anywhere in
// the prompt, not specifically "near" the URL: a prompt short enough to
// contain both a URL and one of these phrases is short enough that "near"
// and "anywhere" come to the same thing in practice, and requiring literal
// adjacency would make this fragile against ordinary word order ("check
// this out: <url>" vs "<url> — check this out").
var referenceCuePhrases = []string{
	"this link", "this site", "this page", "like this", "reference",
	"similar to", "check", "look at", "clone", "inspired by",
}

// referenceURLDominanceThreshold is how much of the prompt's own
// non-whitespace characters the URL itself must account for to be treated
// as a reference on its own, with no cue phrase needed — a prompt that's
// essentially just a pasted link ("https://example.com" or
// "https://example.com  can you access this?") unambiguously means that
// link, the same way attaching a file unambiguously means that file.
const referenceURLDominanceThreshold = 0.6

// ExtractReferenceURL is ExtractFirstURL plus an intent check. Without it,
// ANY URL appearing anywhere in a prompt was treated as a reference to
// fetch — "our shop is at https://example.com — make the header blue"
// pulled down a whole unrelated page and injected external-link framing
// into a request that had nothing to do with it. Returns a URL only when
// the prompt actually seems to be pointing at it: either the URL dominates
// the prompt's own text (see referenceURLDominanceThreshold), or the
// prompt contains a referring cue (see referenceCuePhrases). A plain
// mention with neither returns false, and the turn runs with no reference
// at all — the same "answer only what was actually asked" bias the rest of
// this feature already follows.
//
// This is a stopgap, not the intended long-term UX: the durable fix is an
// explicit "add link" affordance in the dashboard composer, matching the
// attach-file chip already used for image/HTML uploads, which makes intent
// unambiguous instead of inferred from prose. That's a frontend change,
// out of scope here.
func ExtractReferenceURL(prompt string) (string, bool) {
	url, ok := ExtractFirstURL(prompt)
	if !ok {
		return "", false
	}

	promptChars := countNonSpace(prompt)
	if promptChars > 0 && float64(countNonSpace(url))/float64(promptChars) >= referenceURLDominanceThreshold {
		return url, true
	}

	lower := strings.ToLower(prompt)
	for _, cue := range referenceCuePhrases {
		if strings.Contains(lower, cue) {
			return url, true
		}
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

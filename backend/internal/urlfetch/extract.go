package urlfetch

import (
	"regexp"
	"strings"
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

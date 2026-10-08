package pageintent

import "regexp"

// formattingRe names code formatting only; "spacing", "padding" and "layout" are visual and must not match, or a
// styling request would let whitespace-only rewrites through as a change.
var formattingRe = regexp.MustCompile(`(?i)\b(?:indent(?:ation|ing|ed)?|re-?indent|re-?format(?:ting)?|format(?:ting)? (?:the |this |my )?(?:code|file|css|html|liquid|js|javascript)|` +
	`line[- ]endings?|crlf|tidy (?:up )?(?:the |this |my )?(?:code|file|css|html|liquid|js|javascript)|clean ?up (?:the |this |my )?(?:code|file)|prettify|beautify|whitespace)\b`)

// DetectFormatting reports whether prompt explicitly asks for code formatting, the one case where a whitespace-only
// change is the change the merchant wanted.
func DetectFormatting(prompt string) bool {
	return formattingRe.MatchString(prompt)
}

package themecheck

import "strings"

// WhitespaceOnlyChange reports whether before and after differ only in line endings, indentation, trailing whitespace
// or blank lines. Spacing inside a line is kept: it can sit in a string literal, where it's a real change.
func WhitespaceOnlyChange(before, after string) bool {
	return before != after && normalizeWhitespace(before) == normalizeWhitespace(after)
}

func normalizeWhitespace(content string) string {
	content = strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(content, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	return strings.Join(kept, "\n")
}

package ai

import (
	"regexp"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

const genericTextOnlyNudge = "You must call one of the available tools on every turn — propose_changes if you already have enough " +
	"to finish (even for a simple greeting or question, propose_changes with no file changes, " +
	"answered_question: true, and the reply in `summary` is correct), or a read/explore tool otherwise. " +
	"A plain text reply with no tool call is not valid here."

const textEditNudge = "You wrote the change as text. Put it in propose_changes instead."

// Matches a theme file path the model would write when spelling out an edit as text.
var themeFilePathRe = regexp.MustCompile(`(?:^|[^\w/])(?:pages|components|liquid|css|js|assets|layouts?)/[\w./-]+\.(?:liquid|css|js|json)\b|\b(?:pages|defaults)\.json\b`)

// textOnlyNudge picks the correction for a reply with no tool call: one that wrote code or named a theme file was
// trying to make the edit, so it's told where the edit goes rather than reminded of the tool rule in general.
func textOnlyNudge(text string) string {
	if strings.Contains(text, "```") || themeFilePathRe.MatchString(text) {
		return textEditNudge
	}
	return genericTextOnlyNudge
}

// contentBlockTypes lists a reply's block types in order, so an empty-looking reply shows what it did contain.
func contentBlockTypes(message anthropic.Message) []string {
	types := make([]string, 0, len(message.Content))
	for _, block := range message.Content {
		types = append(types, block.Type)
	}
	return types
}

// blockText joins a reply's text blocks, or its thinking blocks when thinking is true.
func blockText(message anthropic.Message, thinking bool) string {
	var b strings.Builder
	for _, block := range message.Content {
		switch v := block.AsAny().(type) {
		case anthropic.TextBlock:
			if !thinking {
				b.WriteString(v.Text)
			}
		case anthropic.ThinkingBlock:
			if thinking {
				b.WriteString(v.Thinking)
			}
		}
	}
	return b.String()
}

func headRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

package ai

import (
	"strings"
	"testing"
)

// Rule 12 names the translucent-colour slip that caused most theme-token rejections, with the form that passes.
func TestStaticSystemPrompt_TranslucentColourRule(t *testing.T) {
	prompt := staticSystemPromptBlock().Text
	for _, want := range []string{"12. In CSS,", "not even inside a gradient", "var(--header-overlay, rgba(255, 255, 255, 0.08))"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
}

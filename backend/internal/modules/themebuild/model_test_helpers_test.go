package themebuild

import (
	"testing"

	"ai-chat/internal/ai"
	"ai-chat/internal/aicatalog"
)

// newSingleModelGenerator is a real Generator on the one-model catalogue a deploy without AI_MODELS_CONFIG gets.
func newSingleModelGenerator(t *testing.T, url, model, effort, visionModel string) *ai.Generator {
	t.Helper()
	cat, err := aicatalog.FromEnv("k", url, model, effort, visionModel)
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	gen, err := ai.New(cat, func(string) (string, bool) { return "k", true }, 0, ai.StreamTimeouts{})
	if err != nil {
		t.Fatalf("ai.New: %v", err)
	}
	return gen
}

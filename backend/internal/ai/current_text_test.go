package ai

import (
	"encoding/json"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

// TestCurrentText_SkipsUnrecognizedBlockType checks a content block with an unrecognized
// Type (a future SDK addition, or a DeepSeek-specific variant) is silently skipped, never a panic.
func TestCurrentText_SkipsUnrecognizedBlockType(t *testing.T) {
	message := anthropic.Message{
		Content: []anthropic.ContentBlockUnion{
			{Type: "some_future_block_type_this_sdk_does_not_know_about"},
		},
	}

	got := currentText(message) // must not panic
	if got != "" {
		t.Errorf("expected empty text for a message with only an unrecognized block, got %q", got)
	}
}

// TestCurrentText_MixedKnownAndUnrecognizedBlocks checks skipping one unrecognized block
// doesn't disturb real text/thinking blocks. Built via json.Unmarshal, not struct literals,
// since AsText()/AsThinking() decode from captured raw JSON — the real streaming path.
func TestCurrentText_MixedKnownAndUnrecognizedBlocks(t *testing.T) {
	raw := `[
		{"type": "text", "text": "hello "},
		{"type": "some_future_block_type_this_sdk_does_not_know_about"},
		{"type": "thinking", "thinking": "world", "signature": "sig"}
	]`
	var content []anthropic.ContentBlockUnion
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		t.Fatalf("failed to unmarshal test fixture: %v", err)
	}
	message := anthropic.Message{Content: content}

	got := currentText(message) // must not panic
	if got != "hello world" {
		t.Errorf("expected the unrecognized block skipped and both real blocks concatenated, got %q", got)
	}
}

package ai

import (
	"encoding/json"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

// TestCurrentText_SkipsUnrecognizedBlockType is item 7: a content block
// whose Type this SDK version doesn't recognize (a future Anthropic
// addition, or a provider-specific variant via DeepSeek's Anthropic-compat
// endpoint — see currentText's own doc comment) must be silently skipped,
// never a panic. block.AsAny() returns nil for an unmatched Type (see the
// SDK's own ContentBlockUnion.AsAny), which currentText's type switch
// simply doesn't match — this test is the regression guard for that
// contract holding, not an implementation detail worth re-deriving by hand
// at every call site.
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

// TestCurrentText_MixedKnownAndUnrecognizedBlocks confirms an unrecognized
// block alongside real text/thinking blocks doesn't disturb them — skip
// only the one block, not abort the whole message. Built via json.Unmarshal
// (not struct literals) because ContentBlockUnion.AsText()/AsThinking()
// decode from their own captured raw JSON, not from directly-set struct
// fields — the same path a real streamed response takes.
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

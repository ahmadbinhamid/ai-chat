package ai

import (
	"encoding/json"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

func TestStreamModelProgressBytes_CountsToolUse(t *testing.T) {
	raw := `[
		{"type": "tool_use", "id": "toolu_1", "name": "propose_changes", "input": {"summary": "x"}}
	]`
	var content []anthropic.ContentBlockUnion
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msg := anthropic.Message{Content: content}
	if n := streamModelProgressBytes(msg); n == 0 {
		t.Fatal("tool_use-only message must count as model progress (clears TTFT)")
	}
	if currentText(msg) != "" {
		t.Fatal("tool_use-only message should have empty currentText")
	}
}

func TestStreamModelProgressBytes_Empty(t *testing.T) {
	if n := streamModelProgressBytes(anthropic.Message{}); n != 0 {
		t.Fatalf("empty message progress=%d want 0", n)
	}
}

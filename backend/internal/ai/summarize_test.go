package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// summarizeServer answers one non-streaming Messages call with content and records the request body.
func summarizeServer(t *testing.T, content []map[string]any, gotBody *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if err := json.Unmarshal(raw, gotBody); err != nil {
			t.Errorf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_sum", "type": "message", "role": "assistant", "model": "test-model",
			"content": content, "stop_reason": "end_turn", "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
		})
	}))
}

func TestSummarize_SendsThinkingDisabled(t *testing.T) {
	var body map[string]any
	ts := summarizeServer(t, []map[string]any{{"type": "text", "text": "summary"}}, &body)
	defer ts.Close()
	g := newTestGenerator(anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("test-key")))

	if _, err := g.Summarize(context.Background(), []Turn{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("Summarize: %v", err)
	}

	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("expected a thinking object in the request, got %#v", body["thinking"])
	}
	if thinking["type"] != "disabled" {
		t.Errorf("expected thinking.type %q, got %#v", "disabled", thinking["type"])
	}
	if mt, _ := body["max_tokens"].(float64); int64(mt) != summarizeMaxTokens {
		t.Errorf("expected max_tokens %d, got %v", summarizeMaxTokens, body["max_tokens"])
	}
}

func TestSummarize_ReturnsOnlyTextBlocks(t *testing.T) {
	var body map[string]any
	ts := summarizeServer(t, []map[string]any{
		{"type": "thinking", "thinking": "LEAKED REASONING", "signature": "sig"},
		{"type": "text", "text": "The merchant built an about page."},
	}, &body)
	defer ts.Close()
	g := newTestGenerator(anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("test-key")))

	got, err := g.Summarize(context.Background(), []Turn{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if got != "The merchant built an about page." {
		t.Errorf("expected only the text block, got %q", got)
	}
}

func TestSummarize_FakeModeUnchanged(t *testing.T) {
	got, err := NewFake(0).Summarize(context.Background(), make([]Turn, 7))
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if want := "[fake mode summary of 7 turns]"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTextOnly(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"text only", `[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, "ab"},
		{"thinking dropped", `[{"type":"thinking","thinking":"x","signature":"s"},{"type":"text","text":"a"}]`, "a"},
		{"thinking only", `[{"type":"thinking","thinking":"x","signature":"s"}]`, ""},
		{"unknown block skipped", `[{"type":"some_future_block"},{"type":"text","text":"a"}]`, "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var content []anthropic.ContentBlockUnion
			if err := json.Unmarshal([]byte(tt.raw), &content); err != nil {
				t.Fatalf("unmarshal fixture: %v", err)
			}
			if got := textOnly(anthropic.Message{Content: content}); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// modelTurnSSE renders one streamed assistant turn from blocks, each {"type": "thinking"|"text"|"tool_use", ...}.
func modelTurnSSE(msgID string, blocks []map[string]any) string {
	var b strings.Builder
	sseEvent(&b, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": msgID, "type": "message", "role": "assistant", "model": "test-model",
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 0},
		},
	})
	stopReason := "end_turn"
	for i, blk := range blocks {
		var start, delta map[string]any
		switch blk["type"] {
		case "thinking":
			start = map[string]any{"type": "thinking", "thinking": "", "signature": ""}
			delta = map[string]any{"type": "thinking_delta", "thinking": blk["thinking"]}
		case "text":
			start = map[string]any{"type": "text", "text": ""}
			delta = map[string]any{"type": "text_delta", "text": blk["text"]}
		case "tool_use":
			input, _ := json.Marshal(blk["input"])
			start = map[string]any{"type": "tool_use", "id": blk["id"], "name": blk["name"], "input": map[string]any{}}
			delta = map[string]any{"type": "input_json_delta", "partial_json": string(input)}
			stopReason = "tool_use"
		}
		sseEvent(&b, "content_block_start", map[string]any{"type": "content_block_start", "index": i, "content_block": start})
		sseEvent(&b, "content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": delta})
		sseEvent(&b, "content_block_stop", map[string]any{"type": "content_block_stop", "index": i})
	}
	sseEvent(&b, "message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": 5},
	})
	sseEvent(&b, "message_stop", map[string]any{"type": "message_stop"})
	return b.String()
}

// recordingProgress captures everything Generate hands its only outbound channel besides the result.
type recordingProgress struct{ seen strings.Builder }

func (p *recordingProgress) ToolStarted(name string, input json.RawMessage) {
	fmt.Fprintf(&p.seen, "start %s %s\n", name, input)
}

func (p *recordingProgress) ToolFinished(name, summary string, _ error) {
	fmt.Fprintf(&p.seen, "finish %s %s\n", name, summary)
}

// Inverted from TestGenerate_StreamStillEmitsThinkingDeltas: model thinking AND plain text are never
// streamed to the merchant. Thinking leaked reasoning (spec internals, file structure, abandoned
// approaches); text leaked tool calls DeepSeek wrote out as JSON when it failed to emit a real
// tool_use block. Filtering either is a heuristic on adversarial content, so the structured
// ToolProgress step feed is the only merchant-facing channel.
func TestGenerate_DoesNotStreamModelOutputToChat(t *testing.T) {
	const leakedThinking = "LEAKED-REASONING per spec section 4 read components/footer.liquid"
	const leakedText = `LEAKED-TEXT {"name": "propose_changes", "input": {"files": []}}`
	proposeInput := map[string]any{
		"summary": "done", "needs_clarification": false, "answered_question": true,
		"files": []any{}, "page_registry_entry": nil, "layout_links_to_add": []string{}, "layout_scripts_to_add": []string{},
	}

	tests := []struct {
		name  string
		turns []string
	}{
		{"thinking alongside tool calls", []string{
			modelTurnSSE("msg_1", []map[string]any{
				{"type": "thinking", "thinking": leakedThinking},
				{"type": "tool_use", "id": "toolu_1", "name": toolNameReadThemeFile, "input": map[string]any{"paths": []string{"components/footer.liquid"}}},
			}),
			modelTurnSSE("msg_2", []map[string]any{
				{"type": "thinking", "thinking": leakedThinking},
				{"type": "tool_use", "id": "toolu_2", "name": toolNameProposeChanges, "input": proposeInput},
			}),
		}},
		// The suspected production leak: a text-only turn with the tool call written out as JSON,
		// which the loop nudges, followed by a real tool call.
		{"text-written tool call", []string{
			modelTurnSSE("msg_1", []map[string]any{
				{"type": "text", "text": leakedText},
			}),
			modelTurnSSE("msg_2", []map[string]any{
				{"type": "text", "text": leakedText},
				{"type": "tool_use", "id": "toolu_1", "name": toolNameReadThemeFile, "input": map[string]any{"paths": []string{"components/footer.liquid"}}},
			}),
			modelTurnSSE("msg_3", []map[string]any{
				{"type": "tool_use", "id": "toolu_2", "name": toolNameProposeChanges, "input": proposeInput},
			}),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if calls >= len(tt.turns) {
					t.Errorf("unexpected extra model call %d", calls+1)
					return
				}
				fmt.Fprint(w, tt.turns[calls])
				calls++
			}))
			defer ts.Close()
			g := newTestGenerator(anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("test-key")))

			progress := &recordingProgress{}
			toolExec := func(context.Context, string, json.RawMessage) (string, error) { return "<footer></footer>\n", nil }
			if _, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "shop"}, nil, "redesign the footer", nil,
				progress, toolExec, nil); err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if calls != len(tt.turns) {
				t.Fatalf("expected %d model calls, got %d", len(tt.turns), calls)
			}
			seen := progress.seen.String()
			if !strings.Contains(seen, "start "+toolNameReadThemeFile) {
				t.Errorf("expected the step feed to still report the read, got %q", seen)
			}
			if strings.Contains(seen, "LEAKED") {
				t.Errorf("model thinking/text reached the merchant-facing channel: %q", seen)
			}
		})
	}
}

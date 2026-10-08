package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"ai-chat/internal/aicatalog"
)

type sentBody struct {
	System   []struct{ Text string } `json:"system"`
	Messages []struct {
		Role    string            `json:"role"`
		Content []json.RawMessage `json:"content"`
	} `json:"messages"`
	Thinking   *struct{ Type string } `json:"thinking"`
	ToolChoice struct{ Type string }  `json:"tool_choice"`
}

// openRouterLoop runs a turn on the OpenRouter catalogue against a fake that searches until it is forced to propose.
func openRouterLoop(t *testing.T, modelID string) []sentBody {
	t.Helper()
	var mu sync.Mutex
	var sent []sentBody
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b sentBody
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Errorf("decode: %v", err)
		}
		mu.Lock()
		sent = append(sent, b)
		n := len(sent)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if b.ToolChoice.Type == "tool" {
			fmt.Fprint(w, toolUseSSEResponse(fmt.Sprintf("m%d", n), fmt.Sprintf("t%d", n), toolNameProposeChanges, emptyAnswer("Done."), 10, 5))
			return
		}
		fmt.Fprint(w, toolUseSSEResponse(fmt.Sprintf("m%d", n), fmt.Sprintf("t%d", n), "list_theme_files", map[string]any{}, 10, 5))
	}))
	t.Cleanup(ts.Close)
	data, err := os.ReadFile("../../config/ai-models.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	_ = json.Unmarshal(data, &raw)
	raw["providers"].(map[string]any)["openrouter"].(map[string]any)["base_url"] = ts.URL
	data, _ = json.Marshal(raw)
	lookup := func(string) (string, bool) { return "k", true }
	cat, err := aicatalog.Parse(data, lookup)
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(cat, lookup, 0, StreamTimeouts{})
	if err != nil {
		t.Fatal(err)
	}
	toolExec := func(context.Context, string, json.RawMessage) (string, error) { return "pages/home.liquid", nil }
	tc := ThemeContext{ThemeSlug: "demo", Model: aicatalog.Choice{ModelID: modelID}}
	if _, err := g.Generate(context.Background(), tc, nil, "a long task", nil, nil, toolExec, nil); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return sent
}

// Auto's design model has thinking explicitly disabled on every call, since DeepSeek would otherwise reason anyway.
func TestGenerate_DesignModelDisablesThinking(t *testing.T) {
	for i, b := range openRouterLoop(t, "deepseek-flash-design") {
		if b.Thinking == nil || b.Thinking.Type != "disabled" {
			t.Errorf("call %d: want thinking disabled, got %+v", i, b.Thinking)
		}
	}
}

// With force_instruction "messages", a forced round keeps the system prompt identical (so its cache still matches)
// and appends the instruction to the last user message, after its tool results; earlier rounds don't carry it.
func TestGenerate_ForcingInstructionGoesInTheMessages(t *testing.T) {
	sent := openRouterLoop(t, "deepseek-flash")
	firstForced := maxToolIterations - forceProposeWithinLastN
	if len(sent) != firstForced+1 {
		t.Fatalf("want %d calls, got %d", firstForced+1, len(sent))
	}
	forced, normal := sent[firstForced], sent[firstForced-1]
	if len(forced.System) != len(normal.System) || forced.System[len(forced.System)-1].Text != normal.System[len(normal.System)-1].Text {
		t.Errorf("want the forced round's system prompt unchanged")
	}
	last := forced.Messages[len(forced.Messages)-1]
	tail := string(last.Content[len(last.Content)-1])
	if last.Role != "user" || !strings.Contains(tail, "Stop searching and call propose_changes now.") || !strings.Contains(string(last.Content[0]), "tool_result") {
		t.Errorf("want the instruction after the tool results in the last user message, got %s", last.Content)
	}
	for i, b := range sent[:firstForced] {
		for _, m := range b.Messages {
			for _, c := range m.Content {
				if strings.Contains(string(c), "Stop searching") {
					t.Fatalf("call %d (not forced) carries the forcing instruction", i)
				}
			}
		}
	}
}

func TestWithTrailingText(t *testing.T) {
	user := anthropic.NewUserMessage(anthropic.NewToolResultBlock("t1", "ok", false))
	assistant := anthropic.NewAssistantMessage(anthropic.NewTextBlock("hi"))
	tests := []struct {
		name     string
		in       []anthropic.MessageParam
		wantLen  int
		wantLast int
	}{
		{"appended to the last user message", []anthropic.MessageParam{user}, 1, 2},
		{"new user message after an assistant one", []anthropic.MessageParam{user, assistant}, 3, 1},
		{"empty conversation", nil, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := len(user.Content)
			out := withTrailingText(tt.in, "force")
			if len(out) != tt.wantLen || len(out[len(out)-1].Content) != tt.wantLast {
				t.Errorf("got %d messages, last with %d blocks", len(out), len(out[len(out)-1].Content))
			}
			if len(user.Content) != before {
				t.Error("the input conversation was modified")
			}
		})
	}
}

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

// capturedGreetingReply is the exact narration from the "how are you" turn that exhausted the tool loop.
const capturedGreetingReply = `{"name": "propose_changes", "arguments": {"answered_question": true, "files": [],
 "page_registry_entry": null, "layout_links_to_add": [], "layout_scripts_to_add": [],
 "needs_clarification": false, "summary": "I am an AI and do not have feelings,
 but I am here to help you. How can I assist you today?"}}` + "```json\n" + `{ "name": "list_theme_files", "arguments": {} }
` + "```" + `{ "name": "read_theme_file", "arguments": { "paths": ["components/testimonials.liquid"] } }
` + "```"

const validArgs = `{"summary":"Done.","needs_clarification":false,"answered_question":true,"files":[],"page_registry_entry":null,"layout_links_to_add":[],"layout_scripts_to_add":[]}`

func textMessage(texts ...string) anthropic.Message {
	var m anthropic.Message
	for _, t := range texts {
		m.Content = append(m.Content, anthropic.ContentBlockUnion{Type: "text", Text: t})
	}
	return m
}

func TestRecoverProposeFromText(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		wantSummary string // "" = must not recover
	}{
		{"exact capture: first call recovered, later calls ignored", capturedGreetingReply,
			"I am an AI and do not have feelings,\n but I am here to help you. How can I assist you today?"},
		{"fenced, with prose around it", "Here you go:\n```json\n{\"name\":\"propose_changes\",\"arguments\":" + validArgs + "}\n```\nThanks!", "Done."},
		{"malformed JSON", `{"name": "propose_changes", "arguments": {"summary": "x", "files": [}`, ""},
		{"different tool first", `{"name":"list_theme_files","arguments":{}} {"name":"propose_changes","arguments":` + validArgs + `}`, ""},
		{"missing a required key", `{"name":"propose_changes","arguments":{"summary":"x","files":[]}}`, ""},
		{"unknown key", `{"name":"propose_changes","arguments":` + strings.Replace(validArgs, `"summary"`, `"bogus":1,"summary"`, 1) + `}`, ""},
		{"wrong type for files", `{"name":"propose_changes","arguments":` + strings.Replace(validArgs, `"files":[]`, `"files":"none"`, 1) + `}`, ""},
		{"arguments as a JSON string", `{"name":"propose_changes","arguments":"{}"}`, ""},
		{"unrelated JSON", `Your config: {"theme":"dark","colors":{"primary":"#000"}}`, ""},
		{"call nested inside unrelated JSON", `{"example":{"name":"propose_changes","arguments":` + validArgs + `}}`, ""},
		{"plain prose", "Hello! How can I help you today?", ""},
		{"triple backticks inside a string value survive", `{"name":"propose_changes","arguments":` +
			strings.Replace(validArgs, `"summary":"Done."`, `"summary":"use `+"```js"+` fences"`, 1) + `}`, "use ```js fences"},
		{"odd quote count in prose before the call", `Making the hero 5" taller:` + "\n" +
			`{"name":"propose_changes",` + "\n" + `"arguments":` + validArgs + `}`, "Done."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, ok := recoverProposeFromText(textMessage(tt.text))
			if tt.wantSummary == "" {
				if ok {
					t.Fatalf("expected no recovery, got %s", args)
				}
				return
			}
			if !ok {
				t.Fatal("expected recovery")
			}
			result, err := decodeProposeInput(args)
			if err != nil || result.Summary != tt.wantSummary {
				t.Fatalf("got summary %q (err %v), want %q", result.Summary, err, tt.wantSummary)
			}
		})
	}
}

func TestGenerate_RecoversTextProposeWithNoConversation(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, textOnlySSEResponse(fmt.Sprintf("msg_%d", calls), capturedGreetingReply, 50, 10))
	}))
	defer ts.Close()
	g := newTestGenerator(anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("k")))
	toolExec := func(_ context.Context, name string, _ json.RawMessage) (string, error) {
		t.Errorf("the calls after the recovered one must be ignored, but %q ran", name)
		return "", nil
	}

	result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil, "how are you", nil, nil, toolExec, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected the first reply to finish the turn, got %d model calls", calls)
	}
	if !result.AnsweredQuestion || !strings.HasPrefix(result.Summary, "I am an AI") || len(result.Files) != 0 {
		t.Errorf("unexpected result %+v", result)
	}
	if result.Conversation() != nil {
		t.Error("a recovered call has no tool_use ID, so it must return no conversation")
	}
}

// A recovered call whose edit can't be applied gets the failure as a plain user message (no tool_result), and the loop continues.
func TestGenerate_RecoveredMaterializeFailureIsPlainUserMessage(t *testing.T) {
	badEdit := `{"name":"propose_changes","arguments":{"summary":"Edited.","needs_clarification":false,"answered_question":false,` +
		`"files":[{"path":"pages/home.liquid","action":"edit","content":"","edits":[{"old_string":"NOT THERE","new_string":"x"}]}],` +
		`"page_registry_entry":null,"layout_links_to_add":[],"layout_scripts_to_add":[]}}`
	var bodies []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		if len(bodies) == 1 {
			fmt.Fprint(w, textOnlySSEResponse("msg_1", badEdit, 50, 10))
			return
		}
		fmt.Fprint(w, toolUseSSEResponse("msg_2", "toolu_2", "propose_changes", map[string]any{
			"summary": "Answered.", "needs_clarification": false, "answered_question": true, "files": []any{},
			"page_registry_entry": nil, "layout_links_to_add": []string{}, "layout_scripts_to_add": []string{},
		}, 60, 15))
	}))
	defer ts.Close()
	g := newTestGenerator(anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("k")))
	readFile := func(context.Context, string) (string, error) { return "<h1>home</h1>", nil }

	result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil, "edit home", nil, nil, nil, readFile)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(bodies) != 2 || result.Summary != "Answered." {
		t.Fatalf("expected a second call after the failed recovered proposal, got %d calls, result %+v", len(bodies), result)
	}
	if !strings.Contains(bodies[1], "written as text, not a tool call) could not be applied") {
		t.Error("expected the materialization failure as a user message")
	}
	if strings.Contains(bodies[1], "tool_result") {
		t.Error("a recovered call has no ID, so no tool_result may be sent")
	}
}

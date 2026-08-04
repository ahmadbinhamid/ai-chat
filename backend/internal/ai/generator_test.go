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

// sseEvent writes one Anthropic streaming SSE event (event: <type>\ndata:
// <json>\n\n) to b.
func sseEvent(b *strings.Builder, eventType string, data any) {
	encoded, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	fmt.Fprintf(b, "event: %s\ndata: %s\n\n", eventType, encoded)
}

// toolUseSSEResponse renders a complete one-turn SSE stream where the model
// calls exactly one tool with the given input (marshaled whole, not as
// incremental deltas — Accumulate treats the first input_json_delta on an
// empty "{}" input as a full replace, so one delta carrying the whole
// object is a faithful, simpler stand-in for real incremental streaming).
func toolUseSSEResponse(msgID, toolID, toolName string, input any, inputTokens, outputTokens int64) string {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		panic(err)
	}
	var b strings.Builder
	sseEvent(&b, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": msgID, "type": "message", "role": "assistant", "model": "claude-test",
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": inputTokens, "output_tokens": 0},
		},
	})
	sseEvent(&b, "content_block_start", map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "tool_use", "id": toolID, "name": toolName, "input": map[string]any{}},
	})
	sseEvent(&b, "content_block_delta", map[string]any{
		"type": "content_block_delta", "index": 0,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": string(inputJSON)},
	})
	sseEvent(&b, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	sseEvent(&b, "message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "tool_use", "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": outputTokens},
	})
	sseEvent(&b, "message_stop", map[string]any{"type": "message_stop"})
	return b.String()
}

// TestGenerate_ToolLoopReadsThenProposes is phase 2's "Done when" scenario:
// given a fake Anthropic server that first calls read_theme_file and then
// propose_changes, Generate executes the read tool via toolExec, feeds its
// result back, and returns the final Result from propose_changes — without
// ai ever touching themefs itself (toolExec is the only bridge).
func TestGenerate_ToolLoopReadsThenProposes(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")

		switch calls {
		case 1:
			fmt.Fprint(w, toolUseSSEResponse("msg_1", "toolu_1", "read_theme_file",
				map[string]any{"paths": []string{"components/testimonials.liquid"}}, 100, 20))
		case 2:
			fmt.Fprint(w, toolUseSSEResponse("msg_2", "toolu_2", "propose_changes", map[string]any{
				"summary":               "Made testimonials light on the about page.",
				"needs_clarification":   false,
				"files":                 []map[string]any{{"path": "pages/about.liquid", "action": "update", "content": "UPDATED"}},
				"page_registry_entry":   nil,
				"layout_links_to_add":   []string{},
				"layout_scripts_to_add": []string{},
			}, 150, 80))
		default:
			t.Errorf("unexpected 3rd call to the fake Anthropic server")
		}
	}))
	defer ts.Close()

	client := anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("test-key"))
	g := newTestGenerator(client, "claude-test")

	var toolExecCalls []string
	toolExec := func(_ context.Context, name string, input json.RawMessage) (string, error) {
		toolExecCalls = append(toolExecCalls, name)
		if name != toolNameReadThemeFile {
			t.Errorf("unexpected tool call %q", name)
			return "", fmt.Errorf("unexpected tool %q", name)
		}
		return "### components/testimonials.liquid\n<div>light bg</div>", nil
	}

	result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil,
		"make testimonials light with a cream background on the about page", nil, toolExec)
	if err != nil {
		t.Fatalf("Generate returned an error: %v", err)
	}

	if len(toolExecCalls) != 1 || toolExecCalls[0] != toolNameReadThemeFile {
		t.Fatalf("expected exactly one read_theme_file tool execution, got %+v", toolExecCalls)
	}
	if calls != 2 {
		t.Fatalf("expected exactly 2 calls to the Anthropic API, got %d", calls)
	}
	if result.Summary != "Made testimonials light on the about page." {
		t.Errorf("unexpected summary: %q", result.Summary)
	}
	if len(result.Files) != 1 || result.Files[0].Path != "pages/about.liquid" || result.Files[0].Content != "UPDATED" {
		t.Fatalf("unexpected files: %+v", result.Files)
	}
	// Token usage must accumulate across both API calls, not just the final one.
	if result.InputTokens != 250 || result.OutputTokens != 100 {
		t.Errorf("expected accumulated tokens 250/100, got %d/%d", result.InputTokens, result.OutputTokens)
	}
}

// TestNew_BaseURLReachesFakeServer confirms New's baseURL param actually
// wires the SDK client at the target endpoint — this is the whole
// DeepSeek-compatibility story (New(apiKey, baseURL, ...) pointed at
// DeepSeek's documented Anthropic-compat endpoint instead of a fake server
// here): if this test passes, the same tool loop already exercised above
// works unmodified against any Anthropic Messages-API-compatible endpoint,
// not just the real Anthropic API.
func TestNew_BaseURLReachesFakeServer(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, toolUseSSEResponse("msg_1", "toolu_1", "propose_changes", map[string]any{
			"summary": "no changes needed", "needs_clarification": false,
			"files": []map[string]any{}, "page_registry_entry": nil,
			"layout_links_to_add": []string{}, "layout_scripts_to_add": []string{},
		}, 10, 5))
	}))
	defer ts.Close()

	g, err := New("test-key", ts.URL, "claude-test", "", 0)
	if err != nil {
		t.Fatalf("New returned an error: %v", err)
	}
	toolExec := func(context.Context, string, json.RawMessage) (string, error) {
		t.Fatal("no tool call expected")
		return "", nil
	}
	if _, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil, "noop", nil, toolExec); err != nil {
		t.Fatalf("Generate returned an error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected New's baseURL to route the request to the fake server, got %d calls", calls)
	}
}

// TestGenerate_GivesUpAfterMaxIterations verifies the loop doesn't spin
// forever if the model keeps calling read tools and never proposes changes.
func TestGenerate_GivesUpAfterMaxIterations(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, toolUseSSEResponse(fmt.Sprintf("msg_%d", calls), fmt.Sprintf("toolu_%d", calls),
			"list_theme_files", map[string]any{}, 10, 5))
	}))
	defer ts.Close()

	client := anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("test-key"))
	g := newTestGenerator(client, "claude-test")

	toolExec := func(_ context.Context, name string, input json.RawMessage) (string, error) {
		return "[]", nil
	}

	_, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil, "do something", nil, toolExec)
	if err == nil {
		t.Fatal("expected an error once the iteration cap is hit")
	}
	if calls != maxToolIterations {
		t.Errorf("expected exactly %d calls (the iteration cap), got %d", maxToolIterations, calls)
	}
}

// TestGenerate_ForcesProposeChangesNearIterationCeiling verifies the
// budget-aware forcing added alongside the 20->28 maxToolIterations raise:
// once the loop is within forceProposeWithinLastN iterations of the cap, the
// request sent to Claude forces the specific propose_changes tool (not the
// generic "any tool" choice) — so a model that's spent its budget reading
// still gets pushed to commit to a proposal rather than reading indefinitely
// and running out the clock with nothing produced.
func TestGenerate_ForcesProposeChangesNearIterationCeiling(t *testing.T) {
	calls := 0
	forceBoundary := maxToolIterations - forceProposeWithinLastN // 0-indexed iteration where forcing begins
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		iteration := calls - 1 // Generate's loop is 0-indexed
		body, _ := io.ReadAll(r.Body)

		wantForced := iteration >= forceBoundary
		gotForced := strings.Contains(string(body), `"tool_choice":{"name":"propose_changes","type":"tool"}`)
		if wantForced != gotForced {
			t.Errorf("iteration %d: expected forced tool_choice=%v, got forced=%v; body: %s", iteration, wantForced, gotForced, body)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		if wantForced {
			fmt.Fprint(w, toolUseSSEResponse(fmt.Sprintf("msg_%d", calls), fmt.Sprintf("toolu_%d", calls),
				"propose_changes", map[string]any{
					"summary":               "Forced proposal near the iteration ceiling.",
					"needs_clarification":   false,
					"files":                 []map[string]any{{"path": "defaults.json", "action": "update", "content": "{}"}},
					"page_registry_entry":   nil,
					"layout_links_to_add":   []string{},
					"layout_scripts_to_add": []string{},
				}, 10, 5))
			return
		}
		fmt.Fprint(w, toolUseSSEResponse(fmt.Sprintf("msg_%d", calls), fmt.Sprintf("toolu_%d", calls),
			"list_theme_files", map[string]any{}, 10, 5))
	}))
	defer ts.Close()

	client := anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("test-key"))
	g := newTestGenerator(client, "claude-test")

	toolExec := func(_ context.Context, name string, input json.RawMessage) (string, error) {
		return "[]", nil
	}

	result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil, "do something", nil, toolExec)
	if err != nil {
		t.Fatalf("Generate returned an error: %v", err)
	}
	if result.Summary != "Forced proposal near the iteration ceiling." {
		t.Errorf("unexpected summary: %q", result.Summary)
	}
	// It should have taken exactly forceBoundary "list" calls before the
	// first forced call finally proposes — confirms forcing kicked in at the
	// expected iteration rather than earlier or never.
	if calls != forceBoundary+1 {
		t.Errorf("expected %d calls before the model proposed, got %d", forceBoundary+1, calls)
	}
}

// TestGenerate_BrandModeOnlyOffersProposeChanges confirms brand mode's tool
// restriction: even though the fake server would happily answer a
// read_theme_file call, the model is never offered anything but
// propose_changes, so it must call that immediately.
func TestGenerate_BrandModeOnlyOffersProposeChanges(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// The static system prompt's own narration mentions "read_theme_file"
		// by name (rule 8), so check the tools array's JSON shape
		// specifically, not a bare substring of the whole request.
		if !strings.Contains(string(body), `"name":"propose_changes"`) || strings.Contains(string(body), `"name":"read_theme_file"`) {
			t.Errorf("expected only propose_changes to be offered in brand mode, request body: %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, toolUseSSEResponse("msg_1", "toolu_1", "propose_changes", map[string]any{
			"summary":               "Updated brand colors.",
			"needs_clarification":   false,
			"files":                 []map[string]any{{"path": "defaults.json", "action": "update", "content": "{}"}},
			"page_registry_entry":   nil,
			"layout_links_to_add":   []string{},
			"layout_scripts_to_add": []string{},
		}, 50, 20))
	}))
	defer ts.Close()

	client := anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("test-key"))
	g := newTestGenerator(client, "claude-test")

	result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo", GenerationMode: GenerationModeBrand}, nil,
		"make the primary color blue", nil, nil)
	if err != nil {
		t.Fatalf("Generate returned an error: %v", err)
	}
	if len(result.Files) != 1 || result.Files[0].Path != "defaults.json" {
		t.Fatalf("unexpected files: %+v", result.Files)
	}
}

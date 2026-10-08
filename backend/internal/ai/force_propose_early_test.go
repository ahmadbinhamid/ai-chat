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
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

func TestShouldForcePropose(t *testing.T) {
	tests := []struct {
		name      string
		iteration int
		elapsed   time.Duration
		want      bool
	}{
		{name: "first round", iteration: 0, elapsed: 0, want: false},
		{name: "last free round", iteration: forceProposeAfterRounds - 1, elapsed: time.Minute, want: false},
		{name: "12 rounds without a proposal", iteration: forceProposeAfterRounds, elapsed: time.Minute, want: true},
		{name: "3 minutes in, early round", iteration: 4, elapsed: forceProposeAfter, want: true},
		{name: "just under 3 minutes", iteration: 4, elapsed: forceProposeAfter - time.Second, want: false},
		{name: "ceiling", iteration: maxToolIterations - 1, elapsed: 0, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldForcePropose(tt.iteration, tt.elapsed); got != tt.want {
				t.Errorf("shouldForcePropose(%d, %s) = %v, want %v", tt.iteration, tt.elapsed, got, tt.want)
			}
		})
	}
	if forceProposeAfterRounds != 12 || forceProposeAfter != 3*time.Minute || maxToolIterations != 28 {
		t.Error("forcing starts at 12 rounds or 3 minutes, under a 28-round ceiling")
	}
}

// A model that only ever searches: the first forced round is round 12, still lists every tool (so the prompt cache holds)
// but names propose_changes, and the turn ends there with an honest question and no files.
func TestGenerate_ForcedRoundKeepsEveryToolAndNamesProposeChanges(t *testing.T) {
	type request struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		ToolChoice struct {
			Type string `json:"type"`
		} `json:"tool_choice"`
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
	}
	var requests []request
	question := "I checked the add to cart button's data-* hooks, its script registration and the basket request, " +
		"and they look correct. What happens when you click it — does the cart count change at all?"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req request
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests = append(requests, req)
		n := len(requests)
		w.Header().Set("Content-Type", "text/event-stream")
		if req.ToolChoice.Type == "tool" {
			fmt.Fprint(w, toolUseSSEResponse(fmt.Sprintf("msg_%d", n), fmt.Sprintf("toolu_%d", n), toolNameProposeChanges, map[string]any{
				"summary": question, "needs_clarification": true, "files": []any{},
				"page_registry_entry": nil, "layout_links_to_add": []string{}, "layout_scripts_to_add": []string{},
			}, 10, 5))
			return
		}
		fmt.Fprint(w, toolUseSSEResponse(fmt.Sprintf("msg_%d", n), fmt.Sprintf("toolu_%d", n), toolNameReadThemeFile,
			map[string]any{"path": "js/minicart.js"}, 10, 5))
	}))
	defer ts.Close()
	g := newTestGenerator(anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("k")))
	toolExec := func(context.Context, string, json.RawMessage) (string, error) { return "// minicart", nil }

	result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil,
		"the add to cart button does nothing, fix it", nil, nil, toolExec, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if len(requests) != forceProposeAfterRounds+1 {
		t.Fatalf("expected the turn to end on round %d, got %d calls", forceProposeAfterRounds+1, len(requests))
	}
	for i, req := range requests {
		forced := i >= forceProposeAfterRounds
		if !forced {
			if len(req.Tools) < 2 || req.ToolChoice.Type != "any" {
				t.Errorf("call %d: want every tool with tool_choice any, got %+v", i, req)
			}
			continue
		}
		if len(req.Tools) != len(requests[0].Tools) || req.ToolChoice.Type != "tool" {
			t.Errorf("call %d (forced): want the same full tool list with tool_choice propose_changes, got %+v / %+v", i, req.Tools, req.ToolChoice)
		}
		last := req.System[len(req.System)-1].Text
		if last != forceProposeInstruction || !strings.Contains(last, "needs_clarification: true") {
			t.Errorf("call %d (forced): want the forcing instruction last in the system prompt, got %q", i, last)
		}
	}

	if !result.NeedsClarification || len(result.Files) != 0 || result.Summary != question {
		t.Errorf("expected a needs_clarification question with no files, got %+v", result)
	}
}

// DeepSeek calls tools a round doesn't allow; none of those may run, on a normal or a forced round, and each gets an
// error result naming what is allowed. A model that never proposes ends on the fixed question.
func TestGenerate_NeverRunsAToolTheRoundDoesNotAllow(t *testing.T) {
	type request struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Messages []json.RawMessage `json:"messages"`
	}
	var requests []request
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req request
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests = append(requests, req)
		n := len(requests)
		w.Header().Set("Content-Type", "text/event-stream")
		name := toolNameGrepTheme // on a forced round this wasn't offered
		if n == 1 {
			name = "delete_theme" // never offered on any round
		}
		fmt.Fprint(w, toolUseSSEResponse(fmt.Sprintf("msg_%d", n), fmt.Sprintf("toolu_%d", n), name, map[string]any{"pattern": "cart"}, 10, 5))
	}))
	defer ts.Close()
	g := newTestGenerator(anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("k")))
	var ran []string
	toolExec := func(_ context.Context, name string, _ json.RawMessage) (string, error) {
		ran = append(ran, name)
		return "match", nil
	}

	result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil, "still not working", nil, nil, toolExec, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(requests) != maxToolIterations {
		t.Fatalf("expected %d calls, got %d", maxToolIterations, len(requests))
	}
	// Rounds 1..11 ran grep_theme; round 0's delete_theme and every forced round's grep_theme did not run.
	if len(ran) != forceProposeAfterRounds-1 {
		t.Errorf("expected only the %d offered grep_theme calls to run, got %d: %v", forceProposeAfterRounds-1, len(ran), ran)
	}
	for _, name := range ran {
		if name != toolNameGrepTheme {
			t.Errorf("ran a tool that was never offered: %s", name)
		}
	}
	if second := string(requests[1].Messages[len(requests[1].Messages)-1]); !strings.Contains(second, "delete_theme isn't allowed in this round") ||
		!strings.Contains(second, "Allowed now: list_theme_files, read_theme_file, grep_theme, propose_changes.") {
		t.Errorf("expected an error result naming the available tools, got %s", second)
	}
	if !result.NeedsClarification || len(result.Files) != 0 || result.Summary != ExhaustedSearchReply {
		t.Errorf("expected the fixed needs_clarification reply with no files, got %+v", result)
	}
}

func TestForceProposeInstruction_RequiresANamedCause(t *testing.T) {
	for _, want := range []string{
		"Only propose a change you can tie to a specific cause you found in the code.",
		"If you can't name the cause, change nothing: set `needs_clarification: true` and ask the merchant one specific question.",
		"never resubmit a file with only whitespace or line-ending changes as a fix",
	} {
		if !strings.Contains(forceProposeInstruction, want) {
			t.Errorf("forcing instruction missing %q:\n%s", want, forceProposeInstruction)
		}
	}
}

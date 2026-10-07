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

// A model that only ever searches: the first forced round is round 12, offers only propose_changes, and the turn ends
// there with an honest question and no files.
func TestGenerate_ForcedRoundOffersOnlyProposeChanges(t *testing.T) {
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
		if len(req.Tools) == 1 && req.Tools[0].Name == toolNameProposeChanges {
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
		if len(req.Tools) != 1 || req.Tools[0].Name != toolNameProposeChanges || req.ToolChoice.Type != "tool" {
			t.Errorf("call %d (forced): want only propose_changes offered, got %+v", i, req.Tools)
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

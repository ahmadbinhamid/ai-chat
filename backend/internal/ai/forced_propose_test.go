package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// At the budget ceiling the loop forces propose_changes; DeepSeek rejects a named tool_choice while thinking, so that
// call alone must disable thinking. Every earlier call keeps adaptive thinking.
func TestGenerate_ForcedProposeDisablesThinking(t *testing.T) {
	type request struct {
		Thinking   struct{ Type string } `json:"thinking"`
		ToolChoice struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"tool_choice"`
	}
	var requests []request
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req request
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests = append(requests, req)
		w.Header().Set("Content-Type", "text/event-stream")
		n := len(requests)
		if req.ToolChoice.Type == "tool" {
			fmt.Fprint(w, toolUseSSEResponse(fmt.Sprintf("msg_%d", n), fmt.Sprintf("toolu_%d", n), "propose_changes", emptyAnswer("Wrapped up."), 10, 5))
			return
		}
		fmt.Fprint(w, toolUseSSEResponse(fmt.Sprintf("msg_%d", n), fmt.Sprintf("toolu_%d", n), "list_theme_files", map[string]any{}, 10, 5))
	}))
	defer ts.Close()
	g := newTestGenerator(anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("k")))
	toolExec := func(context.Context, string, json.RawMessage) (string, error) { return "pages/home.liquid", nil }

	result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil, "a long task", nil, nil, toolExec, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result.Summary != "Wrapped up." {
		t.Fatalf("expected the forced call's proposal, got %+v", result)
	}

	firstForced := maxToolIterations - forceProposeWithinLastN
	if len(requests) != firstForced+1 {
		t.Fatalf("expected %d calls (the forced one answers), got %d", firstForced+1, len(requests))
	}
	for i, req := range requests {
		forced := i >= firstForced
		switch {
		case forced && (req.ToolChoice.Type != "tool" || req.ToolChoice.Name != toolNameProposeChanges || req.Thinking.Type != "disabled"):
			t.Errorf("call %d (forced): want tool_choice propose_changes with thinking disabled, got %+v", i, req)
		case !forced && (req.ToolChoice.Type != "any" || req.Thinking.Type != "adaptive"):
			t.Errorf("call %d: want tool_choice any with adaptive thinking, got %+v", i, req)
		}
	}
}

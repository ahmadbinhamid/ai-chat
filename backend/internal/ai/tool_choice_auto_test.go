package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"ai-chat/internal/aicatalog"
)

// A provider set to "tool_choice": "auto" requires no tool call on normal rounds; forced rounds still name
// propose_changes with thinking off, since auto alone left Flash and Kimi searching on 2 of 3 forced calls.
func TestGenerate_AutoToolChoiceOnNormalRoundsOnly(t *testing.T) {
	type request struct {
		Thinking   struct{ Type string } `json:"thinking"`
		ToolChoice struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"tool_choice"`
	}
	var mu sync.Mutex
	var requests []request
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req request
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		requests = append(requests, req)
		n := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if req.ToolChoice.Type == "tool" {
			fmt.Fprint(w, toolUseSSEResponse(fmt.Sprintf("msg_%d", n), fmt.Sprintf("toolu_%d", n), toolNameProposeChanges, emptyAnswer("Wrapped up."), 10, 5))
			return
		}
		fmt.Fprint(w, toolUseSSEResponse(fmt.Sprintf("msg_%d", n), fmt.Sprintf("toolu_%d", n), "list_theme_files", map[string]any{}, 10, 5))
	}))
	defer ts.Close()

	data, err := os.ReadFile("../../config/ai-models.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	_ = json.Unmarshal(data, &raw)
	for _, p := range raw["providers"].(map[string]any) {
		p.(map[string]any)["base_url"] = ts.URL
		p.(map[string]any)["tool_choice"] = "auto"
	}
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
	tc := ThemeContext{ThemeSlug: "demo", Model: aicatalog.Choice{ModelID: "deepseek-flash", Effort: "low"}}
	if _, err := g.Generate(context.Background(), tc, nil, "a long task", nil, nil, toolExec, nil); err != nil {
		t.Fatalf("Generate: %v", err)
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
		case !forced && (req.ToolChoice.Type != "auto" || req.Thinking.Type != "adaptive"):
			t.Errorf("call %d: want tool_choice auto with adaptive thinking, got %+v", i, req)
		}
	}
}

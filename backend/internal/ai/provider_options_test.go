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

// openRouterGenerator serves the active OpenRouter catalogue (optionally edited) from a fake provider that records
// each raw request body.
func openRouterGenerator(t *testing.T, edit func(map[string]any)) (*Generator, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var bodies []map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, body)
		n := len(bodies)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, toolUseSSEResponse(fmt.Sprintf("msg_%d", n), fmt.Sprintf("toolu_%d", n), toolNameProposeChanges, emptyAnswer("Done."), 10, 5))
	}))
	t.Cleanup(ts.Close)

	data, err := os.ReadFile("../../config/ai-models.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["providers"].(map[string]any)["openrouter"].(map[string]any)["base_url"] = ts.URL
	if edit != nil {
		edit(raw)
	}
	data, _ = json.Marshal(raw)
	lookup := func(name string) (string, bool) { return "k", name == "AI_API_KEY" }
	cat, err := aicatalog.Parse(data, lookup)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	g, err := New(cat, lookup, 0, StreamTimeouts{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), bodies...)
	}
}

func TestGenerate_SendsTheModelsProviderOptions(t *testing.T) {
	// Flash loses its own options, so it gets only the provider-level ones.
	g, bodies := openRouterGenerator(t, func(raw map[string]any) {
		for _, m := range raw["models"].([]any) {
			if m.(map[string]any)["id"] == "deepseek-flash" {
				delete(m.(map[string]any), "options")
			}
		}
	})
	for _, id := range []string{"deepseek-pro", "deepseek-flash"} {
		if _, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "s", Model: aicatalog.Choice{ModelID: id, Effort: "low"}}, nil, "x", nil, nil, nil, nil); err != nil {
			t.Fatalf("Generate(%s): %v", id, err)
		}
	}
	sent := bodies()
	if len(sent) != 2 {
		t.Fatalf("want 2 requests, got %d", len(sent))
	}
	pro, _ := sent[0]["provider"].(map[string]any)
	if sent[0]["model"] != "deepseek/deepseek-v4-pro" || pro["allow_fallbacks"] != true || pro["data_collection"] != "allow" {
		t.Errorf("want Pro's host routing merged with the provider's options, got %v", sent[0]["provider"])
	}
	if order, _ := pro["order"].([]any); len(order) == 0 {
		t.Errorf("want Pro's preferred hosts sent, got %v", pro)
	}
	flash, _ := sent[1]["provider"].(map[string]any)
	if _, has := flash["order"]; has || flash["data_collection"] != "allow" {
		t.Errorf("want only the provider-level options for a model without its own, got %v", sent[1]["provider"])
	}
	if sent[0]["thinking"] == nil || sent[0]["tools"] == nil {
		t.Errorf("options must add to the request, not replace it: %v", sent[0])
	}
}

func TestGenerate_NoOptionsSendsNoExtraFields(t *testing.T) {
	g, bodies := openRouterGenerator(t, func(raw map[string]any) {
		delete(raw["providers"].(map[string]any)["openrouter"].(map[string]any), "options")
		for _, m := range raw["models"].([]any) {
			delete(m.(map[string]any), "options")
		}
	})
	if _, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "s", Model: aicatalog.Choice{ModelID: "deepseek-pro", Effort: "low"}}, nil, "x", nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, has := bodies()[0]["provider"]; has {
		t.Errorf("want no provider field without options, got %v", bodies()[0]["provider"])
	}
}

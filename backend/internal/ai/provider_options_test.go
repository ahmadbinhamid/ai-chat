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
func openRouterGenerator(t *testing.T, edit func(map[string]any)) (*Generator, func() []map[string]any, func() []http.Header) {
	t.Helper()
	var mu sync.Mutex
	var bodies []map[string]any
	var headers []http.Header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, body)
		headers = append(headers, r.Header.Clone())
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
		}, func() []http.Header {
			mu.Lock()
			defer mu.Unlock()
			return append([]http.Header(nil), headers...)
		}
}

func TestGenerate_SendsTheModelsProviderOptions(t *testing.T) {
	// Pro gets an option of its own on top of the provider's; Flash has only the provider's.
	g, bodies, _ := openRouterGenerator(t, func(raw map[string]any) {
		for _, m := range raw["models"].([]any) {
			switch m.(map[string]any)["id"] {
			case "deepseek-pro":
				m.(map[string]any)["options"] = map[string]any{"provider": map[string]any{"sort": "latency"}}
			case "deepseek-flash":
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
	if sent[0]["model"] != "deepseek/deepseek-v4-pro" || pro["sort"] != "latency" || pro["allow_fallbacks"] != true || pro["data_collection"] != "allow" {
		t.Errorf("want Pro's own option merged with the provider's, got %v", sent[0]["provider"])
	}
	flash, _ := sent[1]["provider"].(map[string]any)
	if _, has := flash["sort"]; has || flash["allow_fallbacks"] != true {
		t.Errorf("want only the provider-level options for a model without its own, got %v", sent[1]["provider"])
	}
	if sent[0]["thinking"] == nil || sent[0]["tools"] == nil {
		t.Errorf("options must add to the request, not replace it: %v", sent[0])
	}
}

// A chat's ID goes in the provider's session header on every call, so OpenRouter keeps the chat on one host.
func TestGenerate_SendsTheSessionHeader(t *testing.T) {
	g, _, headers := openRouterGenerator(t, nil)
	tc := ThemeContext{ThemeSlug: "s", Model: aicatalog.Choice{ModelID: "deepseek-flash", Effort: "low"}, SessionID: "chat-123"}
	if _, err := g.Generate(context.Background(), tc, nil, "x", nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := headers()[0].Get("X-Session-Id"); got != "chat-123" {
		t.Errorf("x-session-id = %q, want the chat ID", got)
	}
}

func TestGenerate_NoOptionsSendsNoExtraFields(t *testing.T) {
	g, bodies, _ := openRouterGenerator(t, func(raw map[string]any) {
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

// The DeepSeek rollback catalogue names no session header, so direct DeepSeek never gets one.
func TestGenerate_NoSessionHeaderWithoutOne(t *testing.T) {
	var got http.Header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, toolUseSSEResponse("m", "t", toolNameProposeChanges, emptyAnswer("Done."), 10, 5))
	}))
	defer ts.Close()
	g := catalogueGenerator(t, client(ts.URL), nil)
	tc := ThemeContext{ThemeSlug: "s", Model: aicatalog.Choice{ModelID: "deepseek-flash", Effort: "low"}, SessionID: "chat-123"}
	if _, err := g.Generate(context.Background(), tc, nil, "x", nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if v := got.Get("X-Session-Id"); v != "" {
		t.Errorf("want no session header for a provider without session_header, got %q", v)
	}
}

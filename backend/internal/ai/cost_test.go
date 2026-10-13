package ai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"ai-chat/internal/aicatalog"
)

func TestCostTotal(t *testing.T) {
	tests := []struct {
		name  string
		calls []struct {
			v  float64
			ok bool
		}
		want *float64
	}{
		{name: "no calls", want: nil},
		{name: "nothing reported", calls: []struct {
			v  float64
			ok bool
		}{{0, false}, {0, false}}, want: nil},
		{name: "summed", calls: []struct {
			v  float64
			ok bool
		}{{0.001, true}, {0.0025, true}}, want: ptrFloat(0.0035)},
		{name: "an unreported call adds nothing", calls: []struct {
			v  float64
			ok bool
		}{{0.001, true}, {9, false}}, want: ptrFloat(0.001)},
		{name: "a reported zero is free, not unknown", calls: []struct {
			v  float64
			ok bool
		}{{0, true}}, want: ptrFloat(0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c costTotal
			for _, call := range tt.calls {
				c.add(call.v, call.ok)
			}
			got := c.value()
			if (got == nil) != (tt.want == nil) || (got != nil && fmt.Sprintf("%.10f", *got) != fmt.Sprintf("%.10f", *tt.want)) {
				t.Errorf("value() = %v, want %v", deref(got), deref(tt.want))
			}
		})
	}
}

func ptrFloat(v float64) *float64 { return &v }

func deref(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// withCost adds OpenRouter's usage.cost to a scripted response's message_delta, as OpenRouter sends it.
func withCost(sse string, cost float64) string {
	return strings.Replace(sse, `"usage":{"output_tokens":`, fmt.Sprintf(`"usage":{"cost":%g,"is_byok":false,"output_tokens":`, cost), 1)
}

func TestGenerate_RecordsTheProvidersCost(t *testing.T) {
	tests := []struct {
		name      string
		responses []string
		want      *float64
	}{
		{
			name:      "reported",
			responses: []string{withCost(toolUseSSEResponse("m1", "t1", toolNameProposeChanges, emptyAnswer("Done."), 10, 5), 0.0021)},
			want:      ptrFloat(0.0021),
		},
		{
			name:      "not reported, as by direct DeepSeek",
			responses: []string{toolUseSSEResponse("m1", "t1", toolNameProposeChanges, emptyAnswer("Done."), 10, 5)},
			want:      nil,
		},
		{
			name: "a failed attempt then a retry counts the retry",
			responses: []string{
				midStreamError("api_error", "Provider returned error"),
				withCost(toolUseSSEResponse("m2", "t2", toolNameProposeChanges, emptyAnswer("Done."), 10, 5), 0.0004),
			},
			want: ptrFloat(0.0004),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			n := 0
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				body := tt.responses[min(n, len(tt.responses)-1)]
				n++
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, body)
			}))
			defer ts.Close()
			g := catalogueGenerator(t, client(ts.URL), nil)
			result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "s", Model: aicatalog.Choice{ModelID: "deepseek-flash", Effort: "low"}}, nil, "x", nil, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if (result.CostUSD == nil) != (tt.want == nil) || (tt.want != nil && fmt.Sprintf("%.10f", *result.CostUSD) != fmt.Sprintf("%.10f", *tt.want)) {
				t.Errorf("CostUSD = %v, want %v", deref(result.CostUSD), deref(tt.want))
			}
		})
	}
}

func TestServedBy(t *testing.T) {
	tests := []struct {
		name, messageStart, want string
	}{
		{"openrouter names the host", `{"id":"m","type":"message","role":"assistant","model":"x","content":[],"provider":"Wafer","usage":{"input_tokens":0,"output_tokens":0}}`, "Wafer"},
		{"other providers don't", `{"id":"m","type":"message","role":"assistant","model":"x","content":[],"usage":{"input_tokens":0,"output_tokens":0}}`, ""},
		{"not a string", `{"id":"m","type":"message","role":"assistant","model":"x","content":[],"provider":{"a":1},"usage":{"input_tokens":0,"output_tokens":0}}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m anthropic.Message
			if err := m.UnmarshalJSON([]byte(tt.messageStart)); err != nil {
				t.Fatal(err)
			}
			if got := servedBy(m); got != tt.want {
				t.Errorf("servedBy = %q, want %q", got, tt.want)
			}
		})
	}
}

// A provider that reports no cost (DeepSeek's own API) is priced from the catalogue; a reported cost always wins.
func TestGenerate_PricesUnreportedCallsFromTheCatalogue(t *testing.T) {
	// No peak block, so the result doesn't depend on the hour the test runs.
	priced := func(raw map[string]any) {
		for _, m := range raw["models"].([]any) {
			if m.(map[string]any)["id"] == "deepseek-flash" {
				m.(map[string]any)["pricing"] = map[string]any{"input": 0.15, "cache_hit_input": 0.003, "output": 0.6}
			}
		}
	}
	tests := []struct {
		name  string
		edit  func(map[string]any)
		reply string
		want  *float64
	}{
		{"unreported, priced from the catalogue", priced,
			toolUseSSEResponse("m1", "t1", toolNameProposeChanges, emptyAnswer("Done."), 1000, 500),
			ptrFloat((1000*0.15 + 500*0.6) / 1_000_000)},
		{"a reported cost wins over catalogue prices", priced,
			withCost(toolUseSSEResponse("m1", "t1", toolNameProposeChanges, emptyAnswer("Done."), 1000, 500), 0.0021),
			ptrFloat(0.0021)},
		{"unreported with no prices stays unknown, never free", nil,
			toolUseSSEResponse("m1", "t1", toolNameProposeChanges, emptyAnswer("Done."), 1000, 500), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, tt.reply)
			}))
			defer ts.Close()
			g := catalogueGenerator(t, client(ts.URL), tt.edit)
			result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "s", Model: aicatalog.Choice{ModelID: "deepseek-flash", Effort: "low"}}, nil, "x", nil, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if (result.CostUSD == nil) != (tt.want == nil) || (tt.want != nil && fmt.Sprintf("%.10f", *result.CostUSD) != fmt.Sprintf("%.10f", *tt.want)) {
				t.Errorf("CostUSD = %v, want %v", deref(result.CostUSD), deref(tt.want))
			}
		})
	}
}

func TestPricedCost_CountsCacheReadsAndWrites(t *testing.T) {
	m := aicatalog.Model{Pricing: &aicatalog.Pricing{Input: 1, CacheHitInput: 0.1, Output: 2}}
	u := anthropic.Usage{InputTokens: 1_000_000, CacheReadInputTokens: 2_000_000, CacheCreationInputTokens: 500_000, OutputTokens: 250_000}
	got, ok := pricedCost(m, u, time.Now())
	// Cache writes are billed like misses: (1M + 0.5M) * 1 + 2M * 0.1 + 0.25M * 2.
	if want := 1.5 + 0.2 + 0.5; !ok || fmt.Sprintf("%.6f", got) != fmt.Sprintf("%.6f", want) {
		t.Fatalf("pricedCost = %v (ok %v), want %v", got, ok, want)
	}
	if _, ok := pricedCost(aicatalog.Model{}, u, time.Now()); ok {
		t.Fatal("a model without prices must not report a cost")
	}
}

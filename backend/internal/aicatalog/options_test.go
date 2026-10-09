package aicatalog

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestMergeOptions(t *testing.T) {
	tests := []struct {
		name       string
		base, over map[string]any
		want       map[string]any
	}{
		{"both empty", nil, nil, nil},
		{"base only", map[string]any{"a": 1.0}, nil, map[string]any{"a": 1.0}},
		{"over only", nil, map[string]any{"a": 1.0}, map[string]any{"a": 1.0}},
		{"over wins on a scalar", map[string]any{"a": 1.0}, map[string]any{"a": 2.0}, map[string]any{"a": 2.0}},
		{
			"nested objects merge key by key",
			map[string]any{"provider": map[string]any{"data_collection": "deny", "order": []any{"x"}}},
			map[string]any{"provider": map[string]any{"order": []any{"y"}, "allow_fallbacks": true}},
			map[string]any{"provider": map[string]any{"data_collection": "deny", "order": []any{"y"}, "allow_fallbacks": true}},
		},
		{"object replaces a scalar", map[string]any{"a": 1.0}, map[string]any{"a": map[string]any{"b": true}}, map[string]any{"a": map[string]any{"b": true}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mergeOptions(tt.base, tt.over); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("mergeOptions = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMergeOptions_LeavesInputsUnchanged(t *testing.T) {
	base := map[string]any{"provider": map[string]any{"data_collection": "deny"}}
	mergeOptions(base, map[string]any{"provider": map[string]any{"order": []any{"y"}}})
	if !reflect.DeepEqual(base, map[string]any{"provider": map[string]any{"data_collection": "deny"}}) {
		t.Errorf("base was modified: %v", base)
	}
}

func TestParse_Options(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		wantErr  string
		want     map[string]any
	}{
		{name: "none", want: nil},
		{name: "model only", model: `{"provider":{"order":["azure"]}}`, want: map[string]any{"provider": map[string]any{"order": []any{"azure"}}}},
		{name: "provider only", provider: `{"provider":{"zdr":true}}`, want: map[string]any{"provider": map[string]any{"zdr": true}}},
		{name: "merged", provider: `{"provider":{"zdr":true}}`, model: `{"provider":{"order":["azure"]}}`,
			want: map[string]any{"provider": map[string]any{"zdr": true, "order": []any{"azure"}}}},
		{name: "not an object", model: `["provider"]`, wantErr: "options must be a JSON object"},
		{name: "null", model: `null`, wantErr: "options must be a JSON object"},
		{name: "reserved key", model: `{"thinking":{"type":"enabled"}}`, wantErr: `option "thinking" is set by the generator`},
		{name: "path-like key", provider: `{"provider.order":["x"]}`, wantErr: "keys must be lowercase letters"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := map[string]any{
				"providers":     map[string]any{"p": map[string]any{"base_url": "https://x", "api_key_env": "AI_API_KEY"}},
				"models":        []any{map[string]any{"id": "m", "label": "M", "provider": "p", "model": "x/m"}},
				"default_model": "m", "summary_model": "m",
			}
			if tt.provider != "" {
				raw["providers"].(map[string]any)["p"].(map[string]any)["options"] = json.RawMessage(tt.provider)
			}
			if tt.model != "" {
				raw["models"].([]any)[0].(map[string]any)["options"] = json.RawMessage(tt.model)
			}
			data, _ := json.Marshal(raw)
			c, err := Parse(data, withKey)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := c.RequestFields("m"); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RequestFields = %v, want %v", got, tt.want)
			}
		})
	}
}

// No model sets a host order, which would switch off OpenRouter's sticky routing; every model may fall back, skips
// the host that never cached, keeps OpenRouter's default data collection, and the provider sends a session header.
func TestOpenRouterCatalogue_RoutesStickily(t *testing.T) {
	data, err := os.ReadFile("../../config/ai-models.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(data, withKey)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := c.Provider("openrouter"); p.SessionHeader == "" {
		t.Error("want a session header so each chat stays on one host")
	}
	// No model sets sort: like order, it ranks hosts afresh (on a rolling 5-minute window) and moved a chat mid-turn.
	for _, m := range c.Models {
		p, _ := c.RequestFields(m.ID)["provider"].(map[string]any)
		if _, sorted := p["sort"]; sorted {
			t.Errorf("model %q: want no sort, got %v", m.ID, p["sort"])
		}
		if _, restricted := p["only"]; restricted {
			t.Errorf("model %q: want no host list, got %v", m.ID, p["only"])
		}
		ignore, _ := p["ignore"].([]any)
		if _, ordered := p["order"]; ordered || p["allow_fallbacks"] != true || p["data_collection"] != "allow" || len(ignore) == 0 {
			t.Errorf("model %q: want no order, fallbacks, ignored hosts and data_collection allow; got %v", m.ID, p)
		}
	}
}

// Measured through OpenRouter for each model: whether it takes thinking settings, sees images, and whether effort changes
// how much it thinks (DeepSeek: no, so one effort; Kimi: yes). Grok reasons on its own and rejects thinking: disabled.
func TestOpenRouterCatalogue_Capabilities(t *testing.T) {
	data, err := os.ReadFile("../../config/ai-models.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(data, withKey)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		thinking, images bool
		efforts          int
	}{
		"deepseek-pro":          {thinking: true, images: false, efforts: 1},
		"deepseek-flash":        {thinking: true, images: false, efforts: 1},
		"deepseek-flash-vision": {thinking: true, images: true, efforts: 1},
		"grok":                  {thinking: false, images: true, efforts: 0},
		"kimi":                  {thinking: true, images: true, efforts: 3},
	}
	if len(c.Models) != len(want) {
		t.Fatalf("want %d models, got %d", len(want), len(c.Models))
	}
	for _, m := range c.Models {
		w, ok := want[m.ID]
		if !ok || m.Thinking != w.thinking || m.Images != w.images || len(m.Efforts) != w.efforts {
			t.Errorf("model %q: thinking=%v images=%v efforts=%v", m.ID, m.Thinking, m.Images, m.Efforts)
		}
		if m.Thinking && m.DefaultEffort != "low" {
			t.Errorf("model %q: default effort %q, want low", m.ID, m.DefaultEffort)
		}
	}
}

func TestParse_ToolChoice(t *testing.T) {
	for _, tt := range []struct {
		value   string
		wantErr bool
	}{{"", false}, {"any", false}, {"auto", false}, {"required", true}, {"none", true}} {
		t.Run(tt.value, func(t *testing.T) {
			raw := map[string]any{
				"providers":     map[string]any{"p": map[string]any{"base_url": "https://x", "api_key_env": "AI_API_KEY", "tool_choice": tt.value}},
				"models":        []any{map[string]any{"id": "m", "label": "M", "provider": "p", "model": "x/m"}},
				"default_model": "m", "summary_model": "m",
			}
			data, _ := json.Marshal(raw)
			if _, err := Parse(data, withKey); (err != nil) != tt.wantErr {
				t.Errorf("tool_choice %q: err = %v, wantErr %v", tt.value, err, tt.wantErr)
			}
		})
	}
}

// Wafer cut two tool-loop calls off at 8,192 tokens mid-reasoning and was the slowest host measured, so the DeepSeek
// models ignore it as well as Cloudflare; a model's ignore list replaces the provider's, so it must repeat Cloudflare.
func TestOpenRouterCatalogue_DeepSeekIgnoresWafer(t *testing.T) {
	data, err := os.ReadFile("../../config/ai-models.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(data, withKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range c.Models {
		p, _ := c.RequestFields(m.ID)["provider"].(map[string]any)
		ignore := fmt.Sprint(p["ignore"])
		if strings.HasPrefix(m.ID, "deepseek-") != strings.Contains(ignore, "wafer") || !strings.Contains(ignore, "cloudflare") {
			t.Errorf("model %q: ignore = %v", m.ID, p["ignore"])
		}
	}
}

// OpenRouter sends message_start before the host has read the prompt, so its idle rule waits for real output; the
// DeepSeek rollback keeps the default, so direct DeepSeek behaves as before.
func TestCatalogues_IdleAfter(t *testing.T) {
	for file, want := range map[string]string{"../../config/ai-models.json": IdleAfterContent, "../../config/ai-models.deepseek.json": ""} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		c, err := Parse(data, withKey)
		if err != nil {
			t.Fatal(err)
		}
		for name, p := range c.Providers {
			if p.IdleAfter != want {
				t.Errorf("%s provider %q: idle_after = %q, want %q", file, name, p.IdleAfter, want)
			}
		}
	}
	data, _ := json.Marshal(map[string]any{
		"providers":     map[string]any{"p": map[string]any{"base_url": "https://x", "api_key_env": "AI_API_KEY", "idle_after": "never"}},
		"models":        []any{map[string]any{"id": "m", "label": "M", "provider": "p", "model": "x/m"}},
		"default_model": "m", "summary_model": "m",
	})
	if _, err := Parse(data, withKey); err == nil || !strings.Contains(err.Error(), "idle_after must be") {
		t.Errorf("want an unknown idle_after rejected, got %v", err)
	}
}

// Thinking is off only on Auto's design route (Flash): a fix turn, or a model the merchant picked, keeps it on.
func TestOpenRouterCatalogue_DesignThinkingOff(t *testing.T) {
	data, err := os.ReadFile("../../config/ai-models.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(data, withKey)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		sel     Selection
		fixTurn bool
		want    bool
	}{
		{"auto design turn", Selection{ModelID: AutoID}, false, true},
		{"auto fix turn", Selection{ModelID: AutoID}, true, false},
		{"flash picked by the merchant", Selection{ModelID: "deepseek-flash", Effort: "low"}, false, false},
		{"kimi picked by the merchant", Selection{ModelID: "kimi", Effort: "low"}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := c.DesignThinkingOff(tt.sel, tt.fixTurn); got != tt.want {
				t.Errorf("DesignThinkingOff = %v, want %v", got, tt.want)
			}
		})
	}
	if design := c.Resolve(Selection{ModelID: AutoID}, false); design.ModelID != "deepseek-flash" {
		t.Errorf("auto design model = %q, want deepseek-flash", design.ModelID)
	}
}

func TestParse_DesignThinkingNeedsAThinkingModel(t *testing.T) {
	data, _ := json.Marshal(map[string]any{
		"providers":     map[string]any{"p": map[string]any{"base_url": "https://x", "api_key_env": "AI_API_KEY"}},
		"models":        []any{map[string]any{"id": "m", "label": "M", "provider": "p", "model": "x/m"}},
		"auto":          map[string]any{"label": "Auto", "design_model": "m", "fix_model": "m", "design_thinking": false},
		"default_model": "auto", "summary_model": "m",
	})
	if _, err := Parse(data, withKey); err == nil || !strings.Contains(err.Error(), "design_thinking false needs") {
		t.Errorf("want design_thinking false rejected on a model without thinking, got %v", err)
	}
}

// The DeepSeek models allow every quantization except 4-bit builds (whose runs looped and missed their caches), keep
// "unknown" so Azure stays available, and ignore Reka (cost) and Mancer (no prompt caching). Grok and Kimi are unchanged.
func TestOpenRouterCatalogue_DeepSeekFullPrecisionHosts(t *testing.T) {
	data, err := os.ReadFile("../../config/ai-models.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(data, withKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range c.Models {
		p, _ := c.RequestFields(m.ID)["provider"].(map[string]any)
		quants := fmt.Sprint(p["quantizations"])
		ignore := fmt.Sprint(p["ignore"])
		if !strings.HasPrefix(m.ID, "deepseek-") {
			if p["quantizations"] != nil {
				t.Errorf("model %q: want no quantization filter, got %v", m.ID, p["quantizations"])
			}
			continue
		}
		for _, banned := range []string{"fp4", "int4"} {
			if strings.Contains(" "+strings.Trim(quants, "[]")+" ", " "+banned+" ") {
				t.Errorf("model %q allows %s: %v", m.ID, banned, quants)
			}
		}
		for _, want := range []string{"fp8", "unknown"} {
			if !strings.Contains(quants, want) {
				t.Errorf("model %q: want %s allowed, got %v", m.ID, want, quants)
			}
		}
		for _, host := range []string{"cloudflare", "wafer", "reka", "mancer"} {
			if !strings.Contains(ignore, host) {
				t.Errorf("model %q: want %s ignored, got %v", m.ID, host, ignore)
			}
		}
		if p["allow_fallbacks"] != true {
			t.Errorf("model %q: want fallbacks allowed, got %v", m.ID, p)
		}
	}
}

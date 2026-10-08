package aicatalog

import (
	"encoding/json"
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
	// "only" restricts to hosts that cached well and serve full-precision builds; unlike "order" it keeps stickiness.
	wantOnly := map[string]int{"deepseek-pro": 2, "deepseek-flash": 2, "deepseek-flash-design": 2, "deepseek-flash-vision": 1}
	for _, m := range c.Models {
		p, _ := c.RequestFields(m.ID)["provider"].(map[string]any)
		if only, _ := p["only"].([]any); len(only) != wantOnly[m.ID] {
			t.Errorf("model %q: want %d allowed hosts, got %v", m.ID, wantOnly[m.ID], p["only"])
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
		// Auto's design route: DeepSeek reasons by default, so it is told not to.
		"deepseek-flash-design": {thinking: false, images: false, efforts: 0},
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
		if m.DisableThinking != (m.ID == "deepseek-flash-design") {
			t.Errorf("model %q: disable_thinking=%v", m.ID, m.DisableThinking)
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

// Auto sends design turns to the no-thinking Flash variant and keeps fix turns on Pro with thinking.
func TestOpenRouterCatalogue_AutoDesignTurnsSkipThinking(t *testing.T) {
	data, err := os.ReadFile("../../config/ai-models.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(data, withKey)
	if err != nil {
		t.Fatal(err)
	}
	design := c.Resolve(Selection{ModelID: AutoID}, false)
	if m, _ := c.Model(design.ModelID); m.Thinking || !m.DisableThinking || m.IsSelectable() {
		t.Errorf("design route: want a hidden model with thinking disabled, got %+v", m)
	}
	fix := c.Resolve(Selection{ModelID: AutoID}, true)
	if m, _ := c.Model(fix.ModelID); !m.Thinking || fix.ModelID != "deepseek-pro" {
		t.Errorf("fix route: want Pro with thinking, got %q %+v", fix.ModelID, m)
	}
}

func TestParse_DisableThinkingAndForceInstruction(t *testing.T) {
	tests := []struct {
		name     string
		provider map[string]any
		model    map[string]any
		wantErr  string
	}{
		{name: "disable_thinking without thinking", model: map[string]any{"disable_thinking": true}},
		{name: "disable_thinking with thinking", model: map[string]any{"disable_thinking": true, "thinking": true, "efforts": []any{"low"}, "default_effort": "low"},
			wantErr: "disable_thinking needs"},
		{name: "force_instruction messages", provider: map[string]any{"force_instruction": "messages"}},
		{name: "force_instruction system", provider: map[string]any{"force_instruction": "system"}},
		{name: "force_instruction unknown", provider: map[string]any{"force_instruction": "tools"}, wantErr: "force_instruction must be"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := map[string]any{"base_url": "https://x", "api_key_env": "AI_API_KEY"}
			for k, v := range tt.provider {
				p[k] = v
			}
			m := map[string]any{"id": "m", "label": "M", "provider": "p", "model": "x/m"}
			for k, v := range tt.model {
				m[k] = v
			}
			data, _ := json.Marshal(map[string]any{"providers": map[string]any{"p": p}, "models": []any{m}, "default_model": "m", "summary_model": "m"})
			_, err := Parse(data, withKey)
			if (tt.wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tt.wantErr)) {
				t.Errorf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

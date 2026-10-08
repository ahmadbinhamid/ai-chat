package aicatalog

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func envWith(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
}

var withKey = envWith(map[string]string{"AI_API_KEY": "k"})

// validCatalogue returns the committed catalogue as a map, so each case can break exactly one thing.
func validCatalogue(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile("../../config/ai-models.json")
	if err != nil {
		t.Fatalf("read committed catalogue: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("decode committed catalogue: %v", err)
	}
	return m
}

func encode(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func model(m map[string]any, i int) map[string]any { return m["models"].([]any)[i].(map[string]any) }

func TestParse_CommittedCatalogueLoads(t *testing.T) {
	c, err := Parse(encode(t, validCatalogue(t)), withKey)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.DefaultModel != AutoID || c.VisionModel != "deepseek-flash-vision" || c.SummaryModel != "deepseek-flash" {
		t.Errorf("unexpected catalogue: %+v", c)
	}
}

func TestParse_RefusesInvalidCatalogues(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(m map[string]any)
		env     func(string) (string, bool)
		wantErr string
	}{
		{name: "unknown provider", mutate: func(m map[string]any) { model(m, 0)["provider"] = "openrouter" }, wantErr: `unknown provider "openrouter"`},
		{name: "default effort outside efforts", mutate: func(m map[string]any) { model(m, 0)["default_effort"] = "max" }, wantErr: `default_effort "max" is not in its efforts`},
		{name: "unknown effort value", mutate: func(m map[string]any) { model(m, 0)["efforts"] = []any{"low", "turbo"} }, wantErr: `unknown effort "turbo"`},
		{name: "missing key variable", mutate: func(map[string]any) {}, env: envWith(nil), wantErr: "AI_API_KEY is not set"},
		{name: "duplicate id", mutate: func(m map[string]any) { model(m, 1)["id"] = "deepseek-pro" }, wantErr: `duplicate model id "deepseek-pro"`},
		{name: "vision model not in list", mutate: func(m map[string]any) { m["vision_model"] = "gpt-vision" }, wantErr: `vision_model "gpt-vision" is not in the model list`},
		{name: "vision model without images", mutate: func(m map[string]any) { m["vision_model"] = "deepseek-flash" }, wantErr: `does not have "images": true`},
		{name: "summary model not in list", mutate: func(m map[string]any) { m["summary_model"] = "nope" }, wantErr: `summary_model "nope" is not in the model list`},
		{name: "auto fix model not in list", mutate: func(m map[string]any) { m["auto"].(map[string]any)["fix_model"] = "nope" }, wantErr: `auto fix_model "nope"`},
		{name: "auto effort not offered", mutate: func(m map[string]any) { m["auto"].(map[string]any)["design_effort"] = "max" }, wantErr: `auto design_effort "max"`},
		{name: "default model not selectable", mutate: func(m map[string]any) { m["default_model"] = "deepseek-flash-vision" }, wantErr: "is not a selectable model"},
		{name: "reserved id", mutate: func(m map[string]any) { model(m, 0)["id"] = "auto" }, wantErr: "is reserved"},
		{name: "efforts without thinking", mutate: func(m map[string]any) { model(m, 0)["thinking"] = false }, wantErr: `efforts need "thinking": true`},
		{name: "unknown field", mutate: func(m map[string]any) { m["api_key"] = "sk-123" }, wantErr: `unknown field "api_key"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := validCatalogue(t)
			tt.mutate(m)
			env := tt.env
			if env == nil {
				env = withKey
			}
			_, err := Parse(encode(t, m), env)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Parse() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestParse_ThinkingFalseModelWithoutEfforts(t *testing.T) {
	m := validCatalogue(t)
	plain := model(m, 0)
	plain["thinking"], plain["efforts"], plain["default_effort"] = false, []any{}, ""
	m["auto"].(map[string]any)["fix_effort"] = ""
	c, err := Parse(encode(t, m), withKey)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got, err := c.Select("deepseek-pro", "")
	if err != nil || got.Effort != "" {
		t.Errorf("a model without thinking selects with no effort, got %+v, %v", got, err)
	}
}

func TestFromEnv_MatchesTheOldGenerator(t *testing.T) {
	c, err := FromEnv("k", "https://api.deepseek.com/anthropic", "deepseek-v4-pro", "xhigh", "deepseek-v4-flash-vision-exp")
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	def := c.Default()
	m, _ := c.Model(def.ModelID)
	if m.Model != "deepseek-v4-pro" || def.Effort != "xhigh" || !m.Thinking || m.Images {
		t.Errorf("default turn = %+v (%+v), want deepseek-v4-pro at xhigh with thinking", def, m)
	}
	if s := c.Summary(); s != def {
		t.Errorf("summaries used the turn model before; got %+v, want %+v", s, def)
	}
	img, switched, ok := c.ForImages(def)
	v, _ := c.Model(img.ModelID)
	if !ok || !switched || v.Model != "deepseek-v4-flash-vision-exp" || img.Effort != "xhigh" {
		t.Errorf("image turns used the vision model at the same effort; got %+v (%+v)", img, v)
	}
	if p := c.Public(); len(p.Models) != 1 || p.Auto != nil {
		t.Errorf("only the one configured model is selectable, got %+v", p)
	}

	noVision, err := FromEnv("k", "", "deepseek-v4-pro", "xhigh", "")
	if err != nil || noVision.SupportsImages() {
		t.Errorf("no AI_VISION_MODEL means no image support, got %v, %v", noVision.SupportsImages(), err)
	}
	if _, err := FromEnv("", "", "deepseek-v4-pro", "xhigh", ""); err == nil {
		t.Error("a missing AI_API_KEY must still refuse to start")
	}
}

func committed(t *testing.T) *Catalog {
	t.Helper()
	c, err := Parse(encode(t, validCatalogue(t)), withKey)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSelect(t *testing.T) {
	c := committed(t)
	tests := []struct {
		model, effort string
		want          Selection
		wantErr       error
	}{
		{"", "", Selection{ModelID: AutoID}, nil},
		{"auto", "", Selection{ModelID: AutoID}, nil},
		{"deepseek-pro", "", Selection{ModelID: "deepseek-pro", Effort: "low"}, nil},
		{"deepseek-pro", "high", Selection{ModelID: "deepseek-pro", Effort: "high"}, nil},
		{"deepseek-pro", "max", Selection{}, ErrEffortNotAllowed},
		{"gpt-5", "", Selection{}, ErrUnknownModel},
		{"deepseek-v4-pro", "", Selection{}, ErrUnknownModel}, // a provider model name is not a catalogue id
		{"deepseek-flash-vision", "", Selection{}, ErrModelNotSelectable},
		{"auto", "high", Selection{}, ErrEffortWithAuto},
	}
	for _, tt := range tests {
		t.Run(tt.model+"/"+tt.effort, func(t *testing.T) {
			got, err := c.Select(tt.model, tt.effort)
			if !errors.Is(err, tt.wantErr) || got != tt.want {
				t.Errorf("Select(%q, %q) = %+v, %v; want %+v, %v", tt.model, tt.effort, got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestResolveAndImages(t *testing.T) {
	c := committed(t)
	if got := c.Resolve(Selection{ModelID: AutoID}, false); got != (Choice{"deepseek-flash", "low"}) {
		t.Errorf("auto design turn = %+v", got)
	}
	if got := c.Resolve(Selection{ModelID: AutoID}, true); got != (Choice{"deepseek-pro", "low"}) {
		t.Errorf("auto fix turn = %+v", got)
	}
	if got := c.Resolve(Selection{ModelID: "deepseek-pro", Effort: "high"}, true); got != (Choice{"deepseek-pro", "high"}) {
		t.Errorf("an explicit pick ignores the fix routing, got %+v", got)
	}
	got, switched, ok := c.ForImages(Choice{"deepseek-pro", "high"})
	if !ok || !switched || got != (Choice{"deepseek-flash-vision", "low"}) {
		t.Errorf("image turn on Pro high = %+v switched=%v ok=%v, want vision at its default effort", got, switched, ok)
	}
	if got := c.Summary(); got != (Choice{"deepseek-flash", "low"}) {
		t.Errorf("summary = %+v", got)
	}
}

func TestPublic_HidesProviderDetails(t *testing.T) {
	p := committed(t).Public()
	var ids []string
	for _, m := range p.Models {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "deepseek-pro,deepseek-flash" || p.Auto == nil || p.Auto.ID != AutoID || p.DefaultModel != AutoID {
		t.Errorf("unexpected public catalogue: %+v", p)
	}
	raw, _ := json.Marshal(p)
	for _, secret := range []string{"deepseek-v4", "api.deepseek.com", "AI_API_KEY", "provider", "vision"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("GET /models must not expose %q: %s", secret, raw)
		}
	}
}

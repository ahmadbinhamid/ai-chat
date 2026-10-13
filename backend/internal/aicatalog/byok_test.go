package aicatalog

import (
	"os"
	"reflect"
	"testing"
)

func loadCatalogue(t *testing.T, path string) *Catalog {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(data, envWith(map[string]string{"AI_API_KEY": "k", "DEEPSEEK_API_KEY": "d"}))
	if err != nil {
		t.Fatalf("Parse(%s): %v", path, err)
	}
	return c
}

var byokDirectModels = map[string]string{"deepseek-pro": "deepseek-v4-pro", "deepseek-flash": "deepseek-flash"}

// ai-models.byok.json is ai-models.json with only Pro and Flash moved to DeepSeek's own API; anything else that
// differs means the two files have drifted and switching AI_MODELS_CONFIG would change more than the DeepSeek host.
func TestBYOKCatalogue_DiffersOnlyInTheDirectDeepSeekModels(t *testing.T) {
	base := loadCatalogue(t, "../../config/ai-models.json")
	byok := loadCatalogue(t, "../../config/ai-models.byok.json")

	direct, ok := byok.Provider("deepseek-direct")
	want := Provider{BaseURL: "https://api.deepseek.com/anthropic", APIKeyEnv: "DEEPSEEK_API_KEY", ToolChoice: "auto", IdleAfter: "content"}
	if !ok || !reflect.DeepEqual(direct, want) {
		t.Fatalf("deepseek-direct = %+v, want %+v (no models_url, session header or options)", direct, want)
	}
	for name, p := range base.Providers {
		if got, ok := byok.Provider(name); !ok || !reflect.DeepEqual(got, p) {
			t.Errorf("provider %q differs between the two files:\n byok %+v\n base %+v", name, got, p)
		}
	}
	if len(byok.Providers) != len(base.Providers)+1 {
		t.Errorf("byok has providers %v; want the base ones plus deepseek-direct only", keys(byok.Providers))
	}

	for id, model := range byokDirectModels {
		m, ok := byok.Model(id)
		if !ok || m.Provider != "deepseek-direct" || m.Model != model || m.Options != nil || byok.RequestFields(id) != nil {
			t.Errorf("model %q = %+v; want deepseek-direct, model %q, no options", id, m, model)
		}
		if m.Pricing == nil {
			t.Errorf("model %q: want published prices, or direct turns record no cost", id)
		}
		b, _ := base.Model(id)
		m.Provider, m.Model, m.Options, m.Pricing = b.Provider, b.Model, b.Options, b.Pricing
		if !reflect.DeepEqual(m, b) {
			t.Errorf("model %q differs beyond provider/model/options/pricing:\n byok %+v\n base %+v", id, m, b)
		}
	}

	if len(byok.Models) != len(base.Models) {
		t.Fatalf("byok has %d models, base %d", len(byok.Models), len(base.Models))
	}
	for i, b := range base.Models {
		if _, direct := byokDirectModels[b.ID]; direct {
			continue
		}
		if !reflect.DeepEqual(byok.Models[i], b) {
			t.Errorf("model %q differs between the two files:\n byok %+v\n base %+v", b.ID, byok.Models[i], b)
		}
	}
	if !reflect.DeepEqual(byok.Auto, base.Auto) || byok.DefaultModel != base.DefaultModel ||
		byok.VisionModel != base.VisionModel || byok.SummaryModel != base.SummaryModel {
		t.Errorf("auto/default/vision/summary differ: byok %+v %q %q %q, base %+v %q %q %q",
			byok.Auto, byok.DefaultModel, byok.VisionModel, byok.SummaryModel,
			base.Auto, base.DefaultModel, base.VisionModel, base.SummaryModel)
	}
}

func TestBYOKCatalogue_NeedsTheDeepSeekKey(t *testing.T) {
	data, err := os.ReadFile("../../config/ai-models.byok.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(data, withKey); err == nil {
		t.Fatal("want ai-models.byok.json refused without DEEPSEEK_API_KEY")
	}
}

func keys(m map[string]Provider) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

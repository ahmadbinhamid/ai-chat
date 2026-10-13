package aicatalog

import "testing"

func TestModel_PreloadFlag(t *testing.T) {
	tests := []struct {
		name  string
		value any // nil leaves the field out
		want  bool
	}{
		{"absent defaults to on", nil, true},
		{"explicit true", true, true},
		{"explicit false", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := validCatalogue(t)
			first := model(m, 0)
			if tt.value != nil {
				first["preload"] = tt.value
			}
			c, err := Parse(encode(t, m), withKey)
			if err != nil {
				t.Fatalf("Parse failed: %v", err)
			}
			got, ok := c.Model(first["id"].(string))
			if !ok {
				t.Fatal("model missing after parse")
			}
			if got.PreloadEnabled() != tt.want {
				t.Fatalf("PreloadEnabled = %v, want %v", got.PreloadEnabled(), tt.want)
			}
		})
	}
}

func TestCommittedCataloguesLeavePreloadOn(t *testing.T) {
	c, err := Parse(encode(t, validCatalogue(t)), withKey)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	for _, m := range c.Models {
		if !m.PreloadEnabled() {
			t.Errorf("model %s has preload switched off; it's a fallback, not a default", m.ID)
		}
	}
}

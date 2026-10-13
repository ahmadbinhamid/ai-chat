package themecheck

import "testing"

func TestCheckImageExists(t *testing.T) {
	snap := Snapshot{Paths: map[string]bool{"images/logo.png": true, "images/staged-hero.jpg": true}}
	tests := []struct {
		name    string
		path    string
		content string
		placed  []string
		want    int
	}{
		{"existing theme image", "pages/home.liquid", `<img src="{{ 'images/logo.png' | asset_url }}" alt="">`, nil, 0},
		{"image staged by an earlier turn", "pages/home.liquid", `<img src="{{ 'images/staged-hero.jpg' | asset_url }}" alt="">`, nil, 0},
		{"saved by this proposal", "pages/home.liquid", `<img src="{{ 'images/hero.jpg' | asset_url }}" alt="">`, []string{"images/hero.jpg"}, 0},
		{"invented image", "pages/home.liquid", `<img src="{{ 'images/espresso-hero.avif' | asset_url }}" alt="">`, nil, 1},
		{"invented image in CSS", "pages/css/home.css", `.hero { background-image: url("{{ "images/roastery-space.avif" | asset_url }}"); }`, nil, 1},
		{"saved image in CSS", "pages/css/home.css", `.hero { background: url("{{ 'images/hero.jpg' | asset_url }}"); }`, []string{"images/hero.jpg"}, 0},
		{"Liquid variable not judged", "components/card.liquid", `<img src="{{ image | asset_url }}" alt="">`, nil, 0},
		{"product image not judged", "components/card.liquid", `<img src="{{ product.image_url }}" alt="">`, nil, 0},
		{"non-image asset not judged", "pages/home.liquid", `<link href="{{ 'css/home.css' | asset_url }}">`, nil, 0},
		{"JS file not judged", "js/hero.js", `const src = "{{ 'images/missing.jpg' | asset_url }}";`, nil, 0},
		{"two invented images", "pages/home.liquid", "{{ 'images/a.jpg' | asset_url }}\n{{ 'images/b.jpg' | asset_url }}", nil, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Proposal{Files: []ProposedFile{{Path: tt.path, Action: "create", Content: tt.content}}, PlacedImages: tt.placed}
			got := checkImageExists(p, snap)
			if len(got) != tt.want {
				t.Fatalf("got %d findings, want %d: %+v", len(got), tt.want, got)
			}
			for _, f := range got {
				if f.Rule != ruleIDImageExists || f.Severity != SeverityError || f.Line < 1 {
					t.Errorf("unexpected finding %+v", f)
				}
			}
		})
	}
}

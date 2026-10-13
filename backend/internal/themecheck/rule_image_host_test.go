package themecheck

import "testing"

func TestCheckImageHost(t *testing.T) {
	// Only platform hosts: stock photos are saved into the theme, so no provider host is ever allowed.
	allowed := map[string]bool{"cdn.flowpos.example": true}
	tests := []struct {
		name    string
		path    string
		content string
		want    int
	}{
		{"theme asset", "pages/home.liquid", `<img src="{{ 'images/hero.jpg' | asset_url }}" alt="Hero">`, 0},
		{"relative path", "pages/home.liquid", `<img src="/theme-assets/images/a.png" alt="">`, 0},
		{"product data", "components/card.liquid", `<img src="{{ product.image_url }}" alt="{{ product.name }}">`, 0},
		{"hotlinked Pixabay photo", "pages/home.liquid", `<img src="https://pixabay.com/get/a_1280.jpg" alt="Beans">`, 1},
		{"hotlinked Pixabay CDN photo", "pages/home.liquid", `<img src="https://cdn.pixabay.com/photo/a_1280.jpg" alt="Beans">`, 1},
		{"hotlinked Pexels photo", "pages/home.liquid", `<img src="https://images.pexels.com/photos/1/a.jpeg" alt="Beans">`, 1},
		{"saved stock photo", "pages/home.liquid", `<img src="{{ 'images/hero-coffee.jpg' | asset_url }}" srcset="{{ 'images/hero-coffee.jpg' | asset_url }} 1280w" sizes="100vw" alt="Beans">`, 0},
		{"platform host", "pages/home.liquid", `<img src="https://cdn.flowpos.example/p.png" alt="">`, 0},
		{"liquid-built host", "pages/home.liquid", `<img src="https://{{ store.domain }}/logo.png" alt="">`, 0},
		{"data URI", "pages/home.liquid", `<img src="data:image/svg+xml;base64,AAAA" alt="">`, 0},
		{"other host", "pages/home.liquid", `<img src="https://picsum.photos/800/600" alt="">`, 1},
		{"protocol-relative other host", "pages/home.liquid", `<img alt="" src='//example.com/a.jpg'>`, 1},
		{"other host in srcset", "pages/home.liquid", `<img src="https://cdn.flowpos.example/a.jpg" srcset="https://evil.example/a.jpg 2x" alt="">`, 1},
		{"invented platform-like host", "pages/home.liquid", `<img src="https://cdn.flowpos.example.evil.example/a.jpg" alt="">`, 1},
		{"invented host in CSS background-image", "pages/css/home.css", `.hero { background-image: url("https://example.com/a.jpg"); }`, 1},
		{"invented host in bare CSS url", "css/base.css", `.hero { background: url(https://example.com/a.jpg) center/cover; }`, 1},
		{"Pixabay URL in CSS", "pages/css/home.css", `.hero { background-image: url('https://pixabay.com/get/a_1280.jpg'); }`, 1},
		{"platform host in CSS", "pages/css/home.css", `.hero { background-image: url('https://cdn.flowpos.example/a.jpg'); }`, 0},
		{"relative URL in CSS", "pages/css/home.css", `.hero { background: url(../images/hero.jpg); }`, 0},
		{"theme-assets path in CSS", "pages/css/home.css", `.hero { background: url("/theme-assets/images/hero.jpg"); }`, 0},
		{"data URI in CSS", "css/base.css", `.icon { background: url("data:image/svg+xml;utf8,<svg/>"); }`, 0},
		{"inline style with an unknown host", "pages/home.liquid", `<section style="background-image: url('https://example.com/bg.jpg')">`, 1},
		{"inline style with asset_url", "pages/home.liquid", `<section style="background-image: url({{ 'images/bg.jpg' | asset_url }})">`, 0},
		{"inline style with a Liquid-built host", "pages/home.liquid", `<section style='background: url("https://{{ store.domain }}/bg.jpg")'>`, 0},
		{"inline style with Pixabay", "pages/home.liquid", `<div style="background:url(https://cdn.pixabay.com/photo/a.jpg)"></div>`, 1},
		{"JS file not judged", "js/hero.js", `el.style.backgroundImage = "url(https://example.com/a.jpg)";`, 0},
		{"two bad images", "pages/home.liquid", "<img src=\"https://a.example/1.jpg\" alt=\"\">\n<IMG SRC=\"http://b.example/2.jpg\" alt=\"\">", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkImageHost(Proposal{Files: []ProposedFile{{Path: tt.path, Action: "create", Content: tt.content}}}, Snapshot{ImageHosts: allowed})
			if len(got) != tt.want {
				t.Fatalf("got %d findings, want %d: %+v", len(got), tt.want, got)
			}
			for _, f := range got {
				if f.Rule != ruleIDImageHost || f.Severity != SeverityError || f.Line < 1 {
					t.Errorf("unexpected finding %+v", f)
				}
			}
		})
	}
}

package themecheck

import "testing"

func TestCheckImageHost(t *testing.T) {
	allowed := map[string]bool{"images.pexels.com": true, "cdn.flowpos.example": true}
	tests := []struct {
		name    string
		path    string
		content string
		want    int
	}{
		{"theme asset", "pages/home.liquid", `<img src="{{ 'images/hero.jpg' | asset_url }}" alt="Hero">`, 0},
		{"relative path", "pages/home.liquid", `<img src="/theme-assets/images/a.png" alt="">`, 0},
		{"product data", "components/card.liquid", `<img src="{{ product.image_url }}" alt="{{ product.name }}">`, 0},
		{"stock provider", "pages/home.liquid", `<img src="https://images.pexels.com/photos/1/a.jpeg?w=1880" alt="Beans">`, 0},
		{"stock srcset", "pages/home.liquid", `<img src="https://images.pexels.com/p/1.jpeg" srcset="https://images.pexels.com/p/1.jpeg?h=350 350w, https://images.pexels.com/p/1.jpeg?w=1880 1880w" sizes="100vw" alt="x">`, 0},
		{"platform host", "pages/home.liquid", `<img src="https://cdn.flowpos.example/p.png" alt="">`, 0},
		{"liquid-built host", "pages/home.liquid", `<img src="https://{{ store.domain }}/logo.png" alt="">`, 0},
		{"data URI", "pages/home.liquid", `<img src="data:image/svg+xml;base64,AAAA" alt="">`, 0},
		{"other host", "pages/home.liquid", `<img src="https://picsum.photos/800/600" alt="">`, 1},
		{"protocol-relative other host", "pages/home.liquid", `<img alt="" src='//example.com/a.jpg'>`, 1},
		{"other host in srcset", "pages/home.liquid", `<img src="https://images.pexels.com/a.jpeg" srcset="https://evil.example/a.jpg 2x" alt="">`, 1},
		{"invented provider-like host", "pages/home.liquid", `<img src="https://images.pexels.com.evil.example/a.jpg" alt="">`, 1},
		{"css file not judged", "css/base.css", `.hero { background: url(https://example.com/a.jpg); }`, 0},
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

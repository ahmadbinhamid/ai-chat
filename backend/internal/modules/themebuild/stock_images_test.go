package themebuild

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ai-chat/internal/ai"
	"ai-chat/internal/stockimages"
	"ai-chat/internal/themefs"
)

type fakeStock struct {
	calls  int
	tenant uint64
	in     stockimages.Input
	imgs   []stockimages.Image
	err    error
}

func (f *fakeStock) Search(_ context.Context, tenantID uint64, in stockimages.Input) ([]stockimages.Image, error) {
	f.calls++
	f.tenant, f.in = tenantID, in
	return f.imgs, f.err
}

func TestStockImagesFor(t *testing.T) {
	tests := []struct {
		name  string
		stock bool
		in    GenerateInput
		want  bool
	}{
		{"redesign", true, GenerateInput{redesign: true}, true},
		{"new page by prompt", true, GenerateInput{Prompt: "create an about us page"}, true},
		{"pages mode", true, GenerateInput{Mode: ai.GenerationModePages, Prompt: "x"}, true},
		{"ordinary edit", true, GenerateInput{Prompt: "make the button red"}, false},
		{"redesign in brand mode", true, GenerateInput{Mode: ai.GenerationModeBrand, redesign: true}, false},
		{"redesign in copy mode", true, GenerateInput{Mode: ai.GenerationModeCopy, redesign: true}, false},
		{"no provider configured", false, GenerateInput{redesign: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{}
			if tt.stock {
				s.SetStockImages(&fakeStock{}, stockimages.Host)
			}
			if got := s.stockImagesFor(tt.in); got != tt.want {
				t.Errorf("stockImagesFor = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestToolExec_SearchStockImages(t *testing.T) {
	img := stockimages.Image{URL: "https://images.pexels.com/p/1.jpeg", URLSmall: "https://images.pexels.com/p/1s.jpeg", Alt: "Beans",
		Photographer: "Ana", PhotographerURL: "https://www.pexels.com/@ana"}
	tests := []struct {
		name      string
		offered   bool
		input     string
		stock     *fakeStock
		wantErr   string
		wantCalls int
	}{
		{"returns the provider's images", true, `{"query":"coffee beans","count":2}`, &fakeStock{imgs: []stockimages.Image{img}}, "", 1},
		{"no results", true, `{"query":"coffee beans"}`, &fakeStock{}, "", 1},
		{"not offered this turn", false, `{"query":"coffee beans"}`, &fakeStock{}, "unknown tool", 0},
		{"invalid input never reaches the provider", true, `{"query":"https://evil.example/a.jpg"}`, &fakeStock{}, "not a URL", 0},
		{"provider failure", true, `{"query":"coffee"}`, &fakeStock{err: stockimages.ErrRateLimited}, "continue with the theme's own images", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{}
			s.SetStockImages(tt.stock, stockimages.Host)
			exec := s.buildToolExecutorWithPreload(nil, themefs.RequestAuth{}, toolOptions{tenantID: 77, stockImages: tt.offered})
			out, err := exec(context.Background(), ai.ToolNameSearchStockImages, json.RawMessage(tt.input))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if tt.stock.calls != tt.wantCalls {
				t.Fatalf("provider calls = %d, want %d", tt.stock.calls, tt.wantCalls)
			}
			if tt.wantCalls == 0 || err != nil {
				return
			}
			if tt.stock.tenant != 77 {
				t.Errorf("searched as tenant %d, want 77", tt.stock.tenant)
			}
			var got struct {
				Images []stockimages.Image `json:"images"`
			}
			if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
				t.Fatalf("result is not JSON: %s", out)
			}
			if len(got.Images) != len(tt.stock.imgs) || (len(got.Images) == 1 && got.Images[0] != img) {
				t.Errorf("got %+v, want %+v", got.Images, tt.stock.imgs)
			}
		})
	}
}

// The platform hosts and, once enabled, the stock host are what themecheck lets a proposed <img> load from.
func TestImageHosts(t *testing.T) {
	s := &Service{}
	s.SetImageHosts([]string{" CDN.FlowPOS.example ", ""})
	if len(s.imageHosts) != 1 || !s.imageHosts["cdn.flowpos.example"] {
		t.Fatalf("platform hosts = %v", s.imageHosts)
	}
	if s.imageHosts[stockimages.Host] {
		t.Error("the stock host must not be allowed before stock images are enabled")
	}
	s.SetStockImages(&fakeStock{}, stockimages.Host)
	if !s.imageHosts[stockimages.Host] || !s.imageHosts["cdn.flowpos.example"] {
		t.Errorf("hosts after enabling stock = %v", s.imageHosts)
	}
}

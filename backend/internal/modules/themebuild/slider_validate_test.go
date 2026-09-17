package themebuild

import (
	"strings"
	"testing"

	"ai-chat/internal/ai"
)

func TestIncompleteSliderFeatureProposal_CSSOnlyRejected(t *testing.T) {
	err := incompleteSliderFeatureProposal(
		"can we use multiple images on slider and auto scroll please",
		&ai.Result{Files: []ai.GeneratedFile{{
			Path:    "components/css/store-hero-banner.css",
			Action:  "update",
			Content: ".t1-hero{color:red}",
		}}},
	)
	if err == nil {
		t.Fatal("expected CSS-only slider proposal to be rejected")
	}
}

func TestIncompleteSliderFeatureProposal_SelectorMismatchRejected(t *testing.T) {
	err := incompleteSliderFeatureProposal(
		"make the hero a working multi-image slider with autoplay",
		&ai.Result{Files: []ai.GeneratedFile{
			{
				Path:    "components/store-hero-banner.liquid",
				Action:  "update",
				Content: `<section data-component="store-hero-banner"><div data-slide-item class="is-active"></div><div data-slide-item></div></section>`,
			},
			{
				Path:    "js/store-hero-banner.js",
				Action:  "update",
				Content: `document.querySelector('[data-hero-slider]'); setInterval(function(){}, 5000);`,
			},
			{
				Path:    "components/css/store-hero-banner.css",
				Action:  "update",
				Content: `.t1-hero-slide:not(.is-active){display:none!important}`,
			},
		}},
	)
	if err == nil || !strings.Contains(err.Error(), "data-hero-slider") {
		t.Fatalf("expected selector mismatch rejection, got %v", err)
	}
}

func TestIncompleteSliderFeatureProposal_CompleteOK(t *testing.T) {
	err := incompleteSliderFeatureProposal(
		"enable autoplay on the carousel",
		&ai.Result{
			Files: []ai.GeneratedFile{
				{
					Path:   "components/store-hero-banner.liquid",
					Action: "update",
					Content: `<section data-hero-slider data-component="store-hero-banner">
<div data-slide-item class="is-active"></div>
<div data-slide-item></div>
</section>`,
				},
				{
					Path:    "js/store-hero-banner.js",
					Action:  "update",
					Content: "var root=document.querySelector('[data-hero-slider]'); setInterval(function(){ el.classList.toggle('is-active'); }, 5000);",
				},
				{
					Path:    "components/css/store-hero-banner.css",
					Action:  "update",
					Content: `.t1-hero-slide:not(.is-active){display:none!important}`,
				},
				{
					Path:    "liquid/layout-end.liquid",
					Action:  "update",
					Content: `<script src="{{ 'js/store-hero-banner.js' | asset_url }}" defer></script>`,
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("complete slider proposal must pass: %v", err)
	}
}

func TestIncompleteSliderFeatureProposal_NonSliderSkipped(t *testing.T) {
	err := incompleteSliderFeatureProposal(
		"change the header color",
		&ai.Result{Files: []ai.GeneratedFile{{Path: "components/css/header.css", Content: "a{}"}}},
	)
	if err != nil {
		t.Fatalf("non-slider prompts must skip check: %v", err)
	}
}

func TestClassifyIntent_SliderBrokenComplaint(t *testing.T) {
	cases := []string{
		"but home py to slider a e ni rha fucking",
		"slider to ni ye yar ye to iamges",
		"slider not working only images",
	}
	for _, p := range cases {
		got := ClassifyIntent(p, "", false)
		if got != IntentComplexPage {
			t.Errorf("ClassifyIntent(%q)=%s want complex_page", p, got)
		}
		if !isSliderFeaturePrompt(strings.ToLower(p)) {
			t.Errorf("isSliderFeaturePrompt(%q)=false", p)
		}
	}
}

func TestClassifyIntent_SliderNaturalImagesOnly(t *testing.T) {
	p := "koi achi se natural images use kr lo public url use kr lo please fast for slider. or 5 pics ho"
	if !isSliderImagesOnlyPrompt(strings.ToLower(p)) {
		t.Fatal("expected images-only slider prompt")
	}
	if ClassifyIntent(p, "", false) != IntentComplexPage {
		t.Fatalf("want complex_page, got %s", ClassifyIntent(p, "", false))
	}
	if sliderRequestedSlideCount(p) != 5 {
		t.Fatalf("want 5 slides, got %d", sliderRequestedSlideCount(p))
	}
}

func TestIncompleteSliderImagesOnlyProposal(t *testing.T) {
	prompt := "natural images for slider 5 pics"
	err := incompleteSliderFeatureProposal(prompt, &ai.Result{Files: []ai.GeneratedFile{{
		Path: "components/store-hero-banner.liquid",
		Content: `<section data-hero-slider>
<div data-slide-item class="is-active"><img src="https://picsum.photos/id/1/1920/800"/></div>
<div data-slide-item><img src="https://picsum.photos/id/2/1920/800"/></div>
<div data-slide-item><img src="https://picsum.photos/id/3/1920/800"/></div>
<div data-slide-item><img src="https://picsum.photos/id/4/1920/800"/></div>
<div data-slide-item><img src="https://picsum.photos/id/5/1920/800"/></div>
</section>`,
	}}})
	if err != nil {
		t.Fatalf("images-only complete proposal must pass: %v", err)
	}
}

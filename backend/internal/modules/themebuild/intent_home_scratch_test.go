package themebuild

import (
	"strings"
	"testing"
)

func TestClassifyHomepageFromScratch(t *testing.T) {
	p := `Regenerate the entire homepage from scratch as a premium, modern software house / AI technology company website.

Create a complete homepage with:

* A stunning hero section with a 5-slide AI/technology image slider
* Software development services section
* Featured products / SaaS products section
* AI solutions section
* Technologies / tech stack section
* Why choose us section
* Projects / portfolio section
* Client testimonials
* Strong CTA section
* Professional software-house footer

Use a premium, modern, enterprise-level design with strong typography, smooth spacing, subtle animations, modern cards, and a polished responsive layout.

Make the page feel like a real established software company, not a generic template. Use relevant AI, software development, cloud, automation, and technology visuals throughout.`
	got := ClassifyIntent(p, "", false)
	pl := strings.ToLower(strings.Join(strings.Fields(p), " "))
	t.Logf("intent=%s home=%v slider=%v structural=%v", got, isHomePageRedesignPrompt(p), isSliderFeaturePrompt(pl), isPageCreateOrStructural(pl))
	if got != IntentComplexPage {
		t.Fatalf("want complex_page got %s", got)
	}
}

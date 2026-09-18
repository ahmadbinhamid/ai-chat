package themebuild

import (
	"fmt"
	"regexp"
	"strings"

	"ai-chat/internal/ai"
)

// incompleteSliderFeatureProposal rejects CSS-only / static-image stubs when
// the merchant asked for a working multi-image autoplay slider. Observed in
// production: model "succeeds" by trimming CSS, wiring JS to a missing
// data-hero-slider hook, or (worse) removing slides after a "not working"
// complaint.
func incompleteSliderFeatureProposal(prompt string, result *ai.Result) error {
	if result == nil || !isSliderFeaturePrompt(prompt) {
		return nil
	}
	// Full homepage rebuilds include a hero slider as one of many sections —
	// hard-failing the whole changeset for imperfect slider wiring caused a
	// propose→reject→explore-thrash loop ("another pass couldn't finish")
	// that discarded an otherwise usable pages/home.liquid proposal.
	if isFullHomePageRedesignPrompt(prompt) {
		return nil
	}
	if result.NeedsClarification || result.AnsweredQuestion {
		return nil
	}

	// Image-swap only: just need N slides with https <img src> in liquid.
	if isSliderImagesOnlyPrompt(prompt) {
		return incompleteSliderImagesOnlyProposal(prompt, result)
	}

	var liquid, js, css, layout string
	layoutTouched := false
	for _, f := range result.Files {
		low := strings.ToLower(f.Path)
		body := generatedFileBody(f)
		switch {
		case strings.Contains(low, "store-hero-banner") && strings.HasSuffix(low, ".liquid"):
			liquid = body
		case strings.Contains(low, "store-hero-banner") && strings.HasSuffix(low, ".js"):
			js = body
		case strings.Contains(low, "store-hero-banner") && strings.HasSuffix(low, ".css"):
			css = body
		case strings.Contains(low, "layout-end") && strings.HasSuffix(low, ".liquid"):
			layout = body
			layoutTouched = true
		}
	}
	for _, s := range result.LayoutScriptsToAdd {
		if strings.Contains(s, "store-hero-banner.js") {
			layout = s
			layoutTouched = true
		}
	}

	missing := make([]string, 0, 5)
	slideCount := strings.Count(liquid, "data-slide-item")
	if liquid != "" && slideCount < 2 {
		missing = append(missing, "multi-slide store-hero-banner.liquid (2+ data-slide-item) — do not remove slides")
	}
	if liquid == "" {
		missing = append(missing, "store-hero-banner.liquid with 2+ data-slide-item slides")
	}
	if strings.TrimSpace(js) == "" ||
		!(strings.Contains(js, "setInterval") || strings.Contains(strings.ToLower(js), "autoplay") || strings.Contains(js, "is-active")) {
		missing = append(missing, "working js/store-hero-banner.js autoplay")
	}
	if layoutTouched && !strings.Contains(layout, "store-hero-banner.js") {
		missing = append(missing, "layout-end.liquid script include for store-hero-banner.js")
	}
	if liquid != "" && js != "" && !sliderRootSelectorAligned(liquid, js) {
		missing = append(missing, "JS root selector must match an attribute on the hero liquid root (e.g. both need data-hero-slider)")
	}
	if css != "" && !sliderCSSHidesInactive(css) {
		missing = append(missing, "CSS must hide inactive slides (:not(.is-active))")
	}

	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("incomplete slider autoplay proposal — still missing: %s", strings.Join(missing, "; "))
}

func incompleteSliderImagesOnlyProposal(prompt string, result *ai.Result) error {
	want := sliderRequestedSlideCount(prompt)
	var liquid string
	for _, f := range result.Files {
		low := strings.ToLower(f.Path)
		if strings.Contains(low, "store-hero-banner") && strings.HasSuffix(low, ".liquid") {
			liquid = generatedFileBody(f)
			break
		}
	}
	if liquid == "" {
		return fmt.Errorf("incomplete slider image swap — need components/store-hero-banner.liquid only")
	}
	slides := strings.Count(liquid, "data-slide-item")
	httpsImgs := strings.Count(liquid, `src="https://`) + strings.Count(liquid, `src='https://`)
	if slides < want {
		return fmt.Errorf("incomplete slider image swap — need %d data-slide-item slides, got %d", want, slides)
	}
	if httpsImgs < want {
		return fmt.Errorf("incomplete slider image swap — need %d public https img src URLs, got %d", want, httpsImgs)
	}
	if !strings.Contains(liquid, "data-hero-slider") {
		return fmt.Errorf("incomplete slider image swap — keep data-hero-slider on the root element")
	}
	return nil
}

var jsRootSelectorRe = regexp.MustCompile(`querySelector\(\s*['"]\[([^\]]+)\]['"]\s*\)`)

func sliderRootSelectorAligned(liquid, js string) bool {
	if strings.Contains(js, "data-hero-slider") {
		return strings.Contains(liquid, "data-hero-slider")
	}
	if m := jsRootSelectorRe.FindStringSubmatch(js); len(m) == 2 {
		attr := m[1]
		key := attr
		if i := strings.IndexByte(attr, '='); i >= 0 {
			key = attr[:i]
		}
		return strings.Contains(liquid, key)
	}
	if strings.Contains(js, "store-hero-banner") {
		return strings.Contains(liquid, "store-hero-banner") || strings.Contains(liquid, "data-component")
	}
	return true
}

func sliderCSSHidesInactive(css string) bool {
	low := strings.ToLower(css)
	return strings.Contains(low, ":not(.is-active)") ||
		(strings.Contains(low, "[hidden]") && strings.Contains(low, "display: none"))
}

func generatedFileBody(f ai.GeneratedFile) string {
	var b strings.Builder
	b.WriteString(f.Content)
	for _, e := range f.Edits {
		b.WriteString(e.OldString)
		b.WriteString(e.NewString)
	}
	return b.String()
}

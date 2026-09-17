package themebuild

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"ai-chat/internal/themefs"
)

// ComplexPageContext is CPU-local context for IntentComplexPage (new page /
// page+menu / homepage redesign). Discovery happens here so DeepSeek does
// not thrash on list/grep/read before proposing.
type ComplexPageContext struct {
	Paths      []string
	Package    string
	Sufficient bool // true when ranked paths cover the request class
}

const (
	maxComplexPagePaths      = 5
	maxComplexPagePkgRunes   = 14_000
	maxComplexPageModelCalls = 4 // prepared path should propose quickly
	maxComplexExploration    = 1 // at most one narrow read before force-propose
	maxComplexExploreStreak  = 1 // one explore-only turn, then force propose
	complexPageExcerptLines  = 60
	// complexPageRecentTurns is how many prior chat turns to replay when
	// PageCreatePrepared — skip Summarize API cost when local theme context
	// already carries the structural state.
	complexPageRecentTurns = 2
)

type pathScore struct {
	path  string
	score int
}

// BuildComplexPageContext selects a compact, intent-aware file package —
// homepage redesign prefers home/hero/slider; page+menu prefers pages + nav;
// never a full theme dump.
func BuildComplexPageContext(ctx context.Context, store themefs.ThemeStore, auth themefs.RequestAuth, prompt string) (ComplexPageContext, error) {
	out := ComplexPageContext{}
	tree, err := store.ListFiles(ctx, auth)
	if err != nil {
		return out, err
	}
	paths := flattenThemePaths(tree)
	ranked := rankPathsForPageCreate(paths, prompt)
	if len(ranked) > maxComplexPagePaths {
		ranked = ranked[:maxComplexPagePaths]
	}
	out.Paths = ranked
	homeRedesign := isHomePageRedesignPrompt(prompt)
	sliderFeature := isSliderFeaturePrompt(prompt)
	sliderImagesOnly := isSliderImagesOnlyPrompt(prompt)
	sectionRedesign := isSectionRedesignPrompt(prompt)
	out.Sufficient = complexPageContextSufficient(ranked, homeRedesign || sliderFeature || sectionRedesign)

	var b strings.Builder
	if sectionRedesign && !sliderFeature && !homeRedesign {
		target := "footer"
		if strings.Contains(strings.ToLower(prompt), "header") && !strings.Contains(strings.ToLower(prompt), "footer") {
			target = "header"
		}
		fmt.Fprintf(&b, "## Pre-selected local %s redesign/fix context\n", target)
		b.WriteString("Merchant wants a full modern redesign OR says the section CSS/design did not apply.\n")
		b.WriteString("Ship BOTH liquid + matching CSS in one propose_changes using action \"update\" with FULL file bodies.\n")
		b.WriteString("CRITICAL: CSS selectors MUST match the liquid class names you emit.\n")
		b.WriteString("If liquid uses new classes (e.g. t1-footer--saas), rewrite footer.css for those classes — do NOT leave old selectors (e.g. t1-footer--jpro) as the only rules.\n")
		b.WriteString("Keep existing theme tokens/brand colors where sensible; stay responsive; do not invent extra pages.\n")
		b.WriteString("Call propose_changes once with the section liquid and its CSS.\n\n")
	} else if sliderImagesOnly {
		b.WriteString("## Pre-selected local hero-slider IMAGE SWAP\n")
		b.WriteString("ONLY change <img src> URLs in components/store-hero-banner.liquid.\n")
		b.WriteString("Keep data-hero-slider, data-slide-item, is-active, CTA markup, JS, CSS, layout unchanged.\n")
		b.WriteString("Use action \"update\" with the FULL liquid file (not tiny edits) so materialization succeeds.\n")
		wantSlides := sliderRequestedSlideCount(prompt)
		fmt.Fprintf(&b, "Provide exactly %d slides (data-slide-item), each with a distinct public https image URL.\n", wantSlides)
		b.WriteString("Preferred public URLs (use these or equivalent https picsum/unsplash):\n")
		for i, u := range sliderPublicImageURLs(wantSlides) {
			fmt.Fprintf(&b, "  %d) %s\n", i+1, u)
		}
		b.WriteString("Call propose_changes once — only store-hero-banner.liquid. Do not touch JS/CSS/layout.\n\n")
	} else if sliderFeature {
		b.WriteString("## Pre-selected local hero-slider context\n")
		b.WriteString("Merchant wants a WORKING multi-image hero slider with autoplay/auto-scroll.\n")
		b.WriteString("Static stacked images are NOT enough. You must ship all of:\n")
		b.WriteString("1) components/store-hero-banner.liquid — 2+ slides (data-slide-item); root MUST include data-hero-slider;\n")
		b.WriteString("   only one slide has is-active. NEVER remove slides when merchant says it is broken.\n")
		b.WriteString("2) js/store-hero-banner.js — querySelector('[data-hero-slider]') + setInterval cycling is-active.\n")
		b.WriteString("3) liquid/layout-end.liquid — script tag for js/store-hero-banner.js (if not already present).\n")
		b.WriteString("4) CSS — hide inactive slides with :not(.is-active){display:none!important}.\n")
		b.WriteString("If js/store-hero-banner.js is empty, FILL IT — do not leave a 0-byte stub.\n")
		b.WriteString("If merchant says slider shows only images / not working: FIX the wiring — do not delete the slider.\n")
		b.WriteString("Call propose_changes promptly with those files. Do not list/grep the theme.\n\n")
	} else if homeRedesign {
		b.WriteString("## Pre-selected local homepage-redesign context\n")
		b.WriteString("Redesign the homepage (slider/hero/layout as requested).\n")
		b.WriteString("Relevant homepage files were selected locally — call propose_changes promptly.\n")
		b.WriteString("Do not re-list or grep the theme. Only change files required for the homepage redesign.\n")
		b.WriteString("Emit a compact changeset: only changed files, no full-file dumps of unchanged assets, one short summary.\n")
		b.WriteString("Typical changeset: pages/home.liquid (+ matching CSS/JS) and any hero/slider partials.\n\n")
	} else {
		b.WriteString("## Pre-selected local page-create context\n")
		b.WriteString("Create or register a new page and wire navigation if requested.\n")
		b.WriteString("Relevant theme conventions were selected locally — call propose_changes promptly.\n")
		b.WriteString("Do not dump unrelated files. Only change files that are required.\n")
		b.WriteString("Emit a compact changeset: only changed files, one short summary.\n")
		b.WriteString("Typical changeset: new page liquid + pages.json entry + menu/nav/header link.\n\n")
	}
	b.WriteString("Merchant request: ")
	b.WriteString(strings.TrimSpace(prompt))
	b.WriteString("\n\n")

	for _, p := range ranked {
		content, rerr := store.ReadFile(ctx, auth, p)
		if rerr != nil {
			fmt.Fprintf(&b, "### %s\nERROR: %v\n\n", p, rerr)
			continue
		}
		if sliderFeature && strings.HasSuffix(strings.ToLower(p), ".js") && strings.TrimSpace(content) == "" {
			fmt.Fprintf(&b, "### %s\n(EMPTY FILE — implement autoplay slider JS here before proposing)\n\n", p)
			continue
		}
		lowPath := strings.ToLower(p)
		// Full footer/header bodies so a SaaS redesign can rewrite columns in one shot.
		if sectionRedesign && (strings.Contains(lowPath, "footer") || strings.Contains(lowPath, "header")) &&
			(strings.HasSuffix(lowPath, ".liquid") || strings.HasSuffix(lowPath, ".css")) {
			fmt.Fprintf(&b, "### %s\n%s\n\n", p, content)
		} else if sliderImagesOnly && strings.HasSuffix(lowPath, ".liquid") {
			fmt.Fprintf(&b, "### %s\n%s\n\n", p, content)
		} else {
			excerpt := truncateLines(content, complexPageExcerptLines)
			fmt.Fprintf(&b, "### %s\n%s\n\n", p, excerpt)
		}
		if len([]rune(b.String())) > maxComplexPagePkgRunes {
			b.WriteString("(additional files omitted to keep context bounded)\n")
			break
		}
	}
	if len(ranked) == 0 {
		b.WriteString("(no ranked files — one targeted read_theme_file if needed, then propose_changes)\n")
	}
	out.Package = b.String()
	return out, nil
}

func complexPagePreparedPrompt(userPrompt string, cpc ComplexPageContext) string {
	if strings.TrimSpace(cpc.Package) == "" {
		return userPrompt
	}
	return cpc.Package + "\n---\n" + strings.TrimSpace(userPrompt)
}

func isHomePageRedesignPrompt(prompt string) bool {
	// Slider multi-image / autoplay uses the same home/hero/slider ranking.
	return pageRedesignRe.MatchString(prompt) || isSliderFeaturePrompt(prompt)
}

func promptWantsHeaderOrNav(prompt string) bool {
	p := strings.ToLower(prompt)
	// Word-boundary only — bare strings.Contains("nav") matches "innovative", etc.
	if headerWordRe.MatchString(p) {
		return true
	}
	return menuNavRe.MatchString(p)
}

var headerWordRe = regexp.MustCompile(`(?i)\bheader\b`)

func complexPageContextSufficient(paths []string, structuralFocus bool) bool {
	if len(paths) == 0 {
		return false
	}
	if structuralFocus {
		for _, p := range paths {
			low := strings.ToLower(p)
			if strings.Contains(low, "pages/home") && strings.HasSuffix(low, ".liquid") {
				return true
			}
			if strings.Contains(low, "hero") || strings.Contains(low, "slider") || strings.Contains(low, "carousel") {
				return true
			}
			if strings.Contains(low, "footer") && (strings.HasSuffix(low, ".liquid") || strings.HasSuffix(low, ".css")) {
				return true
			}
			if strings.Contains(low, "header") && (strings.HasSuffix(low, ".liquid") || strings.HasSuffix(low, ".css")) {
				return true
			}
			if strings.Contains(low, "home") && (strings.HasSuffix(low, ".css") || strings.HasSuffix(low, ".js") || strings.HasSuffix(low, ".liquid")) {
				return true
			}
		}
		return false
	}
	for _, p := range paths {
		base := strings.ToLower(path.Base(p))
		low := strings.ToLower(p)
		if base == "pages.json" {
			return true
		}
		if strings.Contains(low, "pages/") && strings.HasSuffix(low, ".liquid") {
			return true
		}
	}
	return false
}

func rankPathsForPageCreate(paths []string, prompt string) []string {
	p := strings.ToLower(prompt)
	homeRedesign := isHomePageRedesignPrompt(prompt)
	sliderFeature := isSliderFeaturePrompt(prompt)
	sliderImagesOnly := isSliderImagesOnlyPrompt(prompt)
	sectionRedesign := isSectionRedesignPrompt(prompt)
	wantsNav := promptWantsHeaderOrNav(prompt)
	var ranked []pathScore
	seen := map[string]bool{}
	for _, fp := range paths {
		low := strings.ToLower(fp)
		base := strings.ToLower(path.Base(fp))
		score := 0
		switch {
		case base == "pages.json":
			score = 200
		case strings.Contains(low, "pages/") && strings.HasSuffix(low, ".liquid"):
			if strings.Contains(low, "home") || strings.Contains(low, "about") || strings.Contains(low, "contact") {
				score = 120
			} else {
				score = 90
			}
		case strings.Contains(low, "header") || strings.Contains(low, "nav") || strings.Contains(low, "menu"):
			score = 150
		case strings.Contains(low, "footer") && (strings.HasSuffix(low, ".liquid") || strings.HasSuffix(low, ".css") || strings.HasSuffix(low, ".js")):
			score = 140
		case strings.Contains(low, "layout") && strings.HasSuffix(low, ".liquid"):
			score = 70
		case base == "defaults.json":
			score = 40
		}
		if sectionRedesign && !homeRedesign && !sliderFeature {
			wantFooter := strings.Contains(p, "footer")
			wantHeader := strings.Contains(p, "header") && !wantFooter
			switch {
			case wantFooter && strings.Contains(low, "footer") && strings.HasSuffix(low, ".liquid"):
				score = 400
			case wantFooter && strings.Contains(low, "footer") && strings.HasSuffix(low, ".css"):
				score = 390
			case wantFooter && strings.Contains(low, "footer") && strings.HasSuffix(low, ".js"):
				score = 370
			case wantHeader && strings.Contains(low, "header") && strings.HasSuffix(low, ".liquid"):
				score = 400
			case wantHeader && strings.Contains(low, "header") && strings.HasSuffix(low, ".css"):
				score = 390
			default:
				score = 0
			}
		} else if homeRedesign {
			switch {
			case strings.Contains(low, "pages/home") && strings.HasSuffix(low, ".liquid"):
				score = 300
			case (strings.Contains(low, "hero") || strings.Contains(low, "slider") || strings.Contains(low, "carousel")) &&
				(strings.HasSuffix(low, ".liquid") || strings.HasSuffix(low, ".css") || strings.HasSuffix(low, ".js")):
				score = 280
			case strings.Contains(low, "sections/") && strings.Contains(low, "home"):
				score = 250
			case strings.Contains(low, "home") && (strings.HasSuffix(low, ".css") || strings.HasSuffix(low, ".js") || strings.HasSuffix(low, ".liquid")) &&
				!strings.Contains(low, "header") && !strings.Contains(low, "menu") && !strings.Contains(low, "nav"):
				score = 240
			case base == "pages.json":
				// Useful registry hint, but never displace home/hero/slider.
				score = 100
			case strings.Contains(low, "layout") && strings.HasSuffix(low, ".liquid"):
				score = 40
			case strings.Contains(low, "header") || strings.Contains(low, "nav") || strings.Contains(low, "menu"):
				if wantsNav {
					score = 80
				} else {
					score = 0
				}
			default:
				// Drop generic high scores from the first switch (e.g. header=150).
				if score > 0 && (strings.Contains(low, "header") || strings.Contains(low, "menu")) {
					score = 0
				}
			}
		} else if !wantsNav {
			// Page create without an explicit menu/nav ask: keep one nav file
			// useful for wiring, but do not flood the package with headers.
			if strings.Contains(low, "header") || strings.Contains(low, "nav") || strings.Contains(low, "menu") {
				if score > 110 {
					score = 110
				}
			}
		}
		// Working autoplay needs layout script wiring + hero JS, not pages.json / home.liquid.
		// Image-swap-only: only the liquid file — keep the package tiny for fast TTFT.
		if sliderImagesOnly {
			if strings.Contains(low, "store-hero-banner") && strings.HasSuffix(low, ".liquid") {
				score = 400
			} else {
				score = 0
			}
		} else if sliderFeature {
			if strings.Contains(low, "store-hero-banner") && strings.HasSuffix(low, ".js") {
				score = 310
			} else if strings.Contains(low, "store-hero-banner") && strings.HasSuffix(low, ".liquid") {
				score = 305
			} else if strings.Contains(low, "store-hero-banner") && strings.HasSuffix(low, ".css") {
				score = 295
			} else if strings.Contains(low, "layout-end") && strings.HasSuffix(low, ".liquid") {
				score = 290
			} else if base == "testimonials.js" {
				// Compact autoplay reference pattern for the model.
				score = 270
			} else if strings.Contains(low, "pages/home") {
				// home.liquid only renders the component — not needed for slider wiring.
				score = 10
			} else if base == "pages.json" {
				score = 20
			}
		}
		if !sectionRedesign {
			if strings.Contains(p, "contact") && strings.Contains(low, "contact") {
				score += 30
			}
			if strings.Contains(p, "faq") && strings.Contains(low, "faq") {
				score += 30
			}
			if strings.Contains(p, "about") && strings.Contains(low, "about") {
				score += 30
			}
		}
		if score > 0 && !seen[fp] {
			seen[fp] = true
			ranked = append(ranked, pathScore{fp, score})
		}
	}

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].path < ranked[j].path
		}
		return ranked[i].score > ranked[j].score
	})

	var out []string
	pageSample := 0
	headerSample := 0
	for _, r := range ranked {
		low := strings.ToLower(r.path)
		if strings.Contains(low, "pages/") && strings.HasSuffix(low, ".liquid") {
			if pageSample >= 1 {
				continue
			}
			pageSample++
		}
		if homeRedesign && (strings.Contains(low, "header") || strings.Contains(low, "nav") || strings.Contains(low, "menu")) {
			if headerSample >= 1 {
				continue
			}
			headerSample++
		}
		out = append(out, r.path)
		if len(out) >= maxComplexPagePaths {
			break
		}
	}
	return out
}

func truncateLines(content string, maxLines int) string {
	if maxLines <= 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= maxLines {
		return content
	}
	return strings.Join(lines[:maxLines], "\n") + "\n…(truncated)"
}

var sliderCountRe = regexp.MustCompile(`(?i)\b(\d+)\s*(?:pics?|images?|imges|photos?|slides?)\b`)

func sliderRequestedSlideCount(prompt string) int {
	m := sliderCountRe.FindStringSubmatch(prompt)
	if len(m) == 2 {
		n := 0
		fmt.Sscanf(m[1], "%d", &n)
		if n >= 2 && n <= 8 {
			return n
		}
	}
	return 5
}

// sliderPublicImageURLs returns stable public https sample photos (picsum).
func sliderPublicImageURLs(n int) []string {
	ids := []int{1015, 1016, 1018, 1025, 1035, 1039, 1043, 1050}
	if n < 1 {
		n = 5
	}
	if n > len(ids) {
		n = len(ids)
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("https://picsum.photos/id/%d/1920/800", ids[i]))
	}
	return out
}

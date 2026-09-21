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
	maxComplexHomePaths      = 8 // full homepage from-scratch needs home + hero + css
	maxComplexPagePkgRunes   = 14_000
	maxComplexHomePkgRunes   = 16_000 // keep TTFT healthy — full bodies for home+hero only
	maxComplexPageModelCalls = 4 // prepared path should propose quickly
	maxComplexHomeModelCalls = 6
	maxComplexExploration    = 1 // at most one narrow read before force-propose
	maxComplexExploreStreak  = 1 // one explore-only turn, then force propose
	complexPageExcerptLines  = 60
	complexHomeExcerptLines  = 50
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
	pathCap := maxComplexPagePaths
	if isFullHomePageRedesignPrompt(prompt) {
		pathCap = maxComplexHomePaths
	}
	if isBrandScrubOrLegalRewritePrompt(prompt) {
		pathCap = maxComplexHomePaths
	}
	if isBulkPageDeletePrompt(prompt) {
		pathCap = 80
	}
	if isMultiPageCreatePrompt(prompt) {
		pathCap = 6
	}
	if named := promptNamedPageSlug(prompt); named != "" && promptScopesToNamedPage(prompt) && !isFullHomePageRedesignPrompt(prompt) {
		pathCap = 4
	}
	if isBlogOrMetaRewritePrompt(prompt) {
		pathCap = 6
	}
	if len(ranked) > pathCap {
		ranked = ranked[:pathCap]
	}
	if isHomePageRedesignPrompt(prompt) || isFullHomePageRedesignPrompt(prompt) {
		ranked = ensureCanonicalHomePaths(paths, ranked, pathCap)
	}
	if isBlogOrMetaRewritePrompt(prompt) && !isFullHomePageRedesignPrompt(prompt) && !isBulkPageDeletePrompt(prompt) && !isMultiPageCreatePrompt(prompt) {
		ranked = ensureBlogMetaPaths(paths, ranked, pathCap)
	} else if named := promptNamedPageSlug(prompt); named != "" && promptScopesToNamedPage(prompt) && !isFullHomePageRedesignPrompt(prompt) && !isBulkPageDeletePrompt(prompt) {
		ranked = ensureNamedPagePaths(paths, ranked, named, pathCap)
	} else if isBulkPageDeletePrompt(prompt) {
		ranked = ensureBulkDeletePaths(paths, ranked, pathCap)
	} else if isMultiPageCreatePrompt(prompt) {
		ranked = ensureMultiPageCreatePaths(paths, ranked, pathCap)
	} else if isAddToMenuPrompt(prompt) {
		ranked = ensureAddToMenuPaths(paths, ranked, pathCap)
	} else if isBrandScrubOrLegalRewritePrompt(prompt) {
		ranked = ensureBrandScrubPaths(paths, ranked, pathCap)
	}
	out.Paths = ranked
	homeRedesign := isHomePageRedesignPrompt(prompt)
	fullHome := isFullHomePageRedesignPrompt(prompt)
	sliderFeature := isSliderFeaturePrompt(prompt)
	sliderImagesOnly := isSliderImagesOnlyPrompt(prompt)
	sectionRedesign := isSectionRedesignPrompt(prompt)
	brandScrub := isBrandScrubOrLegalRewritePrompt(prompt)
	bulkDelete := isBulkPageDeletePrompt(prompt)
	multiCreate := isMultiPageCreatePrompt(prompt)
	multiCreateN := requestedNewPageCount(prompt)
	// Full homepage rebuild wins over slider-only packaging.
	if fullHome {
		sliderImagesOnly = false
	}
	out.Sufficient = complexPageContextSufficient(ranked, homeRedesign || sliderFeature || sectionRedesign || fullHome || brandScrub || bulkDelete || multiCreate || isAddToMenuPrompt(prompt) || isPageContentRewritePrompt(prompt) || isNamedPageCSSBrokenPrompt(prompt) || isBlogOrMetaRewritePrompt(prompt))

	var b strings.Builder
	if multiCreate && !fullHome && !bulkDelete {
		want := multiCreateN
		batch := multiPageCreateBatchSize(prompt)
		fmt.Fprintf(&b, "## Pre-selected local MULTI-PAGE CREATE context (merchant asked %d; create %d THIS turn via AI)\n", want, batch)
		b.WriteString("You (the model) must author real page content matching the merchant's topic — do NOT invent unrelated filler.\n")
		if want > batch {
			fmt.Fprintf(&b, "Merchant asked for %d pages; THIS TURN create only the first %d. Say the rest can follow in the next message.\n", want, batch)
		}
		b.WriteString("Do this in ONE propose_changes:\n")
		fmt.Fprintf(&b, "1) Create %d files: `pages/<slug>.liquid` (action \"create\") — layout-start + layout-end boilerplate, concise real copy on-topic (≈150–250 words each), unique kebab-case slug.\n", batch)
		b.WriteString("2) Direct-update `pages.json` (FULL body, action \"update\"): keep EVERY existing entry, APPEND one published entry per new page.\n")
		b.WriteString("CRITICAL: `page_registry_entry` registers ONLY ONE page — for N>1 edit pages.json directly.\n")
		b.WriteString("FORBIDDEN: updating `pages/blog.liquid`, `pages/css/blog.css`, `pages/home.liquid`, or card-essentials — those are NOT new pages and cause validation churn.\n")
		b.WriteString("FORBIDDEN: only editing blog.liquid / card-essentials.liquid. FORBIDDEN: Go-style generic unrelated posts when they named a different topic.\n")
		b.WriteString("Call propose_changes promptly with the batch of liquid creates + pages.json update.\n\n")
	} else if isBlogOrMetaRewritePrompt(prompt) && !fullHome && !bulkDelete && !multiCreate {
		b.WriteString("## Pre-selected local BLOG + META/SEO rewrite context\n")
		b.WriteString("Merchant wants blog listing/post copy aligned to a software house AND/OR meta titles scrubbed (remove JPRO / old ecommerce branding from titles).\n")
		b.WriteString("Do this in ONE propose_changes (full file bodies, action update):\n")
		b.WriteString("1) Update `pages/blog.liquid` (+ `pages/css/blog.css` if present) with software-house on-topic blog copy. Keep layout-start/end.\n")
		b.WriteString("2) If they mentioned meta titles / SEO / JPRO titles: update `pages.json` FULL body — keep EVERY route; only rewrite title/meta fields that still show the old brand.\n")
		b.WriteString("FORBIDDEN: inventing dozens of new blog pages. FORBIDDEN: editing home/header/footer/card-essentials unless required for titles.\n")
		b.WriteString("Call propose_changes promptly with the focused blog (+ pages.json when meta titles were requested).\n\n")
	} else if (isPageContentRewritePrompt(prompt) || isNamedPageCSSBrokenPrompt(prompt)) && !fullHome && !bulkDelete && !isAddToMenuPrompt(prompt) {
		named := promptNamedPageSlug(prompt)
		if named == "" {
			named = "the named"
		}
		fmt.Fprintf(&b, "## Pre-selected local PAGE CONTENT rewrite (%s)\n", named)
		fmt.Fprintf(&b, "Merchant wants substantial on-topic content / CSS fix on the **%s** page (software-company copy, theme regenerate, CSS not applying).\n", named)
		fmt.Fprintf(&b, "ONLY update `pages/%s.liquid` (+ matching CSS) with action \"update\" and FULL file bodies.\n", named)
		if named == "products" {
			b.WriteString("Shop route `/shop` IS `pages/products.liquid`.\n")
			b.WriteString("CRITICAL CSS: layout-start links `pages/css/product-list.css` (not only products.css). Update `pages/css/product-list.css` to match liquid class names so styles apply. If you also write `pages/css/products.css`, add it via layout_links_to_add.\n")
			b.WriteString("Remove JPRO / numbing-cream ecommerce branding from THIS page + its CSS.\n")
		}
		b.WriteString("Match their topic (CRM/POS/services/software house — whatever they said). Keep layout-start/end boilerplate.\n")
		if named == "blog" {
			b.WriteString("This IS the blog listing page — update `pages/blog.liquid` (and blog CSS). If meta titles were also requested, include a `pages.json` full-body update for titles only.\n")
		} else {
			b.WriteString("FORBIDDEN: editing card-essentials.liquid, contact-inquiry.liquid, blog.liquid, header, or unrelated pages. FORBIDDEN: simple tiny tweaks / ±0 no-ops that ignore the rewrite ask.\n")
		}
		b.WriteString("Call propose_changes promptly with the named page file(s).\n\n")
	} else if pageCreateRe.MatchString(prompt) && !fullHome && !bulkDelete && !brandScrub {
		b.WriteString("## Pre-selected local SINGLE NEW PAGE create (AI-authored)\n")
		b.WriteString("Merchant wants ONE new page that matches THEIR words (e.g. services list with CRM + POS).\n")
		b.WriteString("Do this in ONE propose_changes:\n")
		b.WriteString("1) Create `pages/<kebab-slug>.liquid` (action \"create\") with layout-start/end + real content for what they asked (services/CRM/POS/etc.).\n")
		b.WriteString("2) Register it: prefer `page_registry_entry` (single page) OR pages.json full-body update keeping every existing route.\n")
		b.WriteString("3) Optional matching `pages/css/<slug>.css` + layout_links_to_add if needed.\n")
		b.WriteString("FORBIDDEN: creating a batch of unrelated blog posts. FORBIDDEN: only editing card-essentials / blog.liquid.\n")
		b.WriteString("Call propose_changes promptly with the new page file + registration.\n\n")
	} else if isAddToMenuPrompt(prompt) && !fullHome && !bulkDelete {
		label := menuLabelFromAddPrompt(prompt)
		b.WriteString("## Pre-selected local ADD-TO-MENU context\n")
		b.WriteString("Merchant wants a nav link added. The live storefront menu is `defaults.json` → `menu.items[]` (rendered by header-menu.liquid).\n")
		if label != "" {
			fmt.Fprintf(&b, "Add an item labeled %q (sensible url e.g. /%s) to menu.items.\n", label, strings.ToLower(strings.ReplaceAll(label, " ", "-")))
		}
		b.WriteString("Do this in ONE propose_changes:\n")
		b.WriteString("1) action \"update\" on `defaults.json` with the FULL file body — keep every existing menu item and top-level key, APPEND the new item to menu.items (id, label, url, children:[]).\n")
		b.WriteString("2) Prefer action \"update\" with the complete JSON (not a tiny edit that can no-op).\n")
		b.WriteString("FORBIDDEN: claiming the menu was updated without the new label appearing under menu.items. FORBIDDEN: only editing header.liquid/CSS without defaults.json.\n")
		b.WriteString("Call propose_changes promptly with defaults.json.\n\n")
	} else if bulkDelete && !fullHome {
		b.WriteString("## Pre-selected local PAGE/FILE DELETE context\n")
		b.WriteString("Merchant asked to DELETE/REMOVE pages and/or theme files (any language). This is NOT a list request and NOT a clarify-only answer.\n")
		b.WriteString("Understand WHICH paths from their words (blog pages, orphan pages/*.liquid not in pages.json, components, etc.).\n")
		b.WriteString("Do this in ONE propose_changes:\n")
		b.WriteString("1) If pages/routes are removed: direct-update `pages.json` (FULL body) — keep every core route (home/shop/product/cart/auth/privacy/terms/contact/faq/about/…). Drop only the entries they meant.\n")
		b.WriteString("2) For each removed page/component FILE: include `{path, action:\"delete\", content:\"\", edits:[]}` so Apply deletes the file from disk. Do not leave orphan liquid files.\n")
		b.WriteString("3) Extra files in pages/ that are NOT registered in pages.json: delete those files when the merchant asks to clean extras/orphans.\n")
		b.WriteString("FORBIDDEN: answering with only a list/table or \"let me know if you want…\". FORBIDDEN: deleting pages.json, defaults.json, home.liquid, or layout-start/end.\n")
		b.WriteString("Call propose_changes promptly with pages.json update + delete actions as needed.\n\n")
		if pagesRaw, readErr := store.ReadFile(ctx, auth, pathPagesJSON); readErr == nil {
			if rows, parseErr := parsePagesJSONRows(pagesRaw); parseErr == nil {
				orphans := orphanPageLiquidPaths(paths, registeredLiquidPaths(rows))
				if len(orphans) > 0 {
					b.WriteString("ORPHAN pages/*.liquid NOT in pages.json (you MUST action:\"delete\" each when cleaning extras):\n")
					max := 40
					if len(orphans) < max {
						max = len(orphans)
					}
					for _, p := range orphans[:max] {
						fmt.Fprintf(&b, "- %s\n", p)
					}
					if len(orphans) > max {
						fmt.Fprintf(&b, "- … and %d more\n", len(orphans)-max)
					}
					b.WriteString("\n")
				}
			}
		}
	} else if brandScrub && !fullHome {
		named := promptNamedPageSlug(prompt)
		if named != "" && promptScopesToNamedPage(prompt) {
			fmt.Fprintf(&b, "## Pre-selected local SINGLE-PAGE edit context (%s)\n", named)
			fmt.Fprintf(&b, "Merchant named the **%s** page. Scope THIS turn to that page only.\n", named)
			fmt.Fprintf(&b, "ONLY edit `pages/%s.liquid` and `pages/css/%s.css` (if present). Full file bodies, action update.\n", named, named)
			b.WriteString("If rewriting for a software/SaaS company: replace old ecommerce/JPRO product copy on THIS page only.\n")
			b.WriteString("FORBIDDEN: editing other pages/*.liquid, header/footer, defaults.json, or SEO/blog pages in this turn.\n")
			b.WriteString("FORBIDDEN: endless list/grep. Call propose_changes promptly with only the named page's files.\n\n")
		} else {
			b.WriteString("## Pre-selected local brand-scrub / legal-page rewrite context\n")
			b.WriteString("Merchant wants privacy/legal copy rewritten for a SOFTWARE company AND/OR old ecommerce brand (JPRO / J Pro / jpronumbingcream) removed.\n")
			b.WriteString("THIS TURN priority (propose these, full file bodies, action update):\n")
			b.WriteString("1) pages/privacy.liquid (+ pages/css/privacy.css if present) — rewrite Privacy Policy for a software/SaaS company (no numbing-cream / JPRO product copy, no jpronumbingcream.co.uk).\n")
			b.WriteString("2) Shared shell if they still say JPRO: components/footer.liquid, components/header.liquid, defaults.json — strip brand names/URLs/classes that expose JPRO to visitors.\n")
			b.WriteString("3) Optionally terms/cookie pages if present in the package.\n")
			b.WriteString("Do NOT try to rewrite every SEO/blog page in one propose_changes — focus on privacy + shared shell this turn.\n")
			b.WriteString("FORBIDDEN: endless list/grep of the theme; reading dozens of files without proposing; leaving \"Privacy Policy for J Pro Numbing Cream\" intact.\n")
			b.WriteString("Call propose_changes promptly with FULL updated file bodies. Prefer update over create.\n\n")
		}
	} else if fullHome {
		b.WriteString("## Pre-selected local FULL homepage redesign context\n")
		b.WriteString("Merchant wants the ENTIRE homepage rebuilt (software/AI company landing), not a tiny tweak.\n")
		if isHomeReferenceClonePrompt(prompt) {
			b.WriteString("REFERENCE CLONE REQUEST: rebuild the homepage to match the attached/fetched reference site (section order, hero treatment, spacing, typography, polish).\n")
			b.WriteString("Extract design intent from the reference — do NOT copy raw HTML/CSS/class names.\n")
			b.WriteString("FORBIDDEN outcomes: spelling fixes (e.g. Bestsellers→Best Sellers), editing only card-essentials.liquid, or any single tiny file change while claiming the homepage matches the reference.\n")
			b.WriteString("You MUST propose a multi-file homepage redesign (home.liquid + home.css + hero liquid/css/js at minimum) in one propose_changes.\n")
		}
		b.WriteString("CANONICAL paths: update pages/home.liquid + pages/css/home.css + components/store-hero-banner.liquid + components/css/store-hero-banner.css + js/store-hero-banner.js together.\n")
		b.WriteString("CRITICAL FAILURE MODE TO AVOID: shipping pages/css/home.css with new class names (e.g. t1-sw-*) while pages/home.liquid still renders old ecommerce components (store-hero-banner with t1-shb-*, product grids). That looks like \"plain HTML / no CSS\" to the merchant.\n")
		b.WriteString("CSS selectors MUST match the liquid class names you emit in the SAME propose_changes. Rewrite liquid and CSS as one matched pair.\n")
		b.WriteString("Do NOT create or edit obsolete duplicates (e.g. components/hero-slider.*) when store-hero-banner exists.\n")
		b.WriteString("Do NOT add a second hero slider script — reuse js/store-hero-banner.js and layout-end wiring.\n")
		b.WriteString("Ship a complete pages/home.liquid (or equivalent) plus hero slider liquid/CSS/JS and any section partials/CSS needed.\n")
		b.WriteString("Include a working 5-slide hero (data-hero-slider + data-slide-item + autoplay JS) with distinct public https image URLs (picsum/unsplash).\n")
		b.WriteString("Build the requested sections in-theme (services, products, AI, tech stack, why us, portfolio, testimonials, CTA).\n")
		b.WriteString("Register any NEW css/js paths via layout_links_to_add / layout_scripts_to_add.\n")
		b.WriteString("page_registry_entry for home must keep type/slug/page = home and status = published.\n")
		b.WriteString("Do NOT refuse or ask for clarification because a live AI chat API is missing — use a polished static/demo chat UI if needed; do not invent backend endpoints.\n")
		b.WriteString("Do NOT ask the merchant to split this into smaller requests — handle the full homepage in one propose_changes.\n")
		b.WriteString("Do NOT touch only one unrelated component (e.g. card-essentials.liquid) and claim CSS was fixed.\n")
		b.WriteString("Prefer action \"update\" with FULL file bodies. Call propose_changes promptly. Do not list/grep the whole theme.\n")
		b.WriteString("Keep footer only if merchant asked to keep it; otherwise include a professional software-house footer.\n\n")
	} else if sectionRedesign && !sliderFeature && !homeRedesign {
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
	} else if sliderFeature && !fullHome {
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

	excerptLines := complexPageExcerptLines
	pkgLimit := maxComplexPagePkgRunes
	if fullHome {
		excerptLines = complexHomeExcerptLines
		pkgLimit = maxComplexHomePkgRunes
	}
	for _, p := range ranked {
		content, rerr := store.ReadFile(ctx, auth, p)
		if rerr != nil {
			fmt.Fprintf(&b, "### %s\nERROR: %v\n\n", p, rerr)
			continue
		}
		if sliderFeature && !fullHome && strings.HasSuffix(strings.ToLower(p), ".js") && strings.TrimSpace(content) == "" {
			fmt.Fprintf(&b, "### %s\n(EMPTY FILE — implement autoplay slider JS here before proposing)\n\n", p)
			continue
		}
		lowPath := strings.ToLower(p)
		// Full footer/header bodies so a SaaS redesign can rewrite columns in one shot.
		if sectionRedesign && (strings.Contains(lowPath, "footer") || strings.Contains(lowPath, "header")) &&
			(strings.HasSuffix(lowPath, ".liquid") || strings.HasSuffix(lowPath, ".css")) {
			fmt.Fprintf(&b, "### %s\n%s\n\n", p, content)
		} else if bulkDelete && (lowPath == "pages.json" || strings.HasSuffix(lowPath, "/pages.json")) {
			fmt.Fprintf(&b, "### %s\n%s\n\n", p, content)
		} else if brandScrub && (strings.Contains(lowPath, "privacy") || strings.Contains(lowPath, "footer") ||
			strings.Contains(lowPath, "header") || strings.Contains(lowPath, "defaults.json") ||
			strings.Contains(lowPath, "terms") || strings.Contains(lowPath, "cookie") ||
			(promptNamedPageSlug(prompt) != "" && strings.Contains(lowPath, promptNamedPageSlug(prompt)))) &&
			(strings.HasSuffix(lowPath, ".liquid") || strings.HasSuffix(lowPath, ".css") || strings.HasSuffix(lowPath, ".json")) {
			fmt.Fprintf(&b, "### %s\n%s\n\n", p, content)
		} else if sliderImagesOnly && strings.HasSuffix(lowPath, ".liquid") {
			fmt.Fprintf(&b, "### %s\n%s\n\n", p, content)
		} else if fullHome && (strings.Contains(lowPath, "pages/home") || strings.Contains(lowPath, "store-hero-banner")) &&
			(strings.HasSuffix(lowPath, ".liquid") || strings.HasSuffix(lowPath, ".css") || strings.HasSuffix(lowPath, ".js")) {
			fmt.Fprintf(&b, "### %s\n%s\n\n", p, content)
		} else {
			excerpt := truncateLines(content, excerptLines)
			fmt.Fprintf(&b, "### %s\n%s\n\n", p, excerpt)
		}
		if len([]rune(b.String())) > pkgLimit {
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
	return pageRedesignRe.MatchString(prompt) || isSliderFeaturePrompt(prompt) || isFullHomePageRedesignPrompt(prompt)
}

// isFullHomePageRedesignPrompt is a whole-homepage rebuild (from scratch /
// SaaS landing with many sections) — must package pages/home.liquid, not
// only the hero-slider wiring files.
func isFullHomePageRedesignPrompt(prompt string) bool {
	p := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
	if isHomeCSSBrokenPrompt(p) {
		return true
	}
	if isHomeReferenceClonePrompt(p) {
		return true
	}
	if pageRedesignRe.MatchString(p) {
		return true
	}
	if !(strings.Contains(p, "homepage") || strings.Contains(p, "home page") ||
		(strings.Contains(p, "home") && strings.Contains(p, "page"))) {
		return false
	}
	return strings.Contains(p, "from scratch") || strings.Contains(p, "entire homepage") ||
		strings.Contains(p, "whole homepage") || strings.Contains(p, "complete homepage") ||
		strings.Contains(p, "landing page") || strings.Contains(p, "software house") ||
		strings.Contains(p, "saas") ||
		(strings.Contains(p, "regenerate") && strings.Contains(p, "home"))
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
			// Any packed page liquid is enough to propose (shop→products,
			// services, privacy, home, …). Requiring only home/privacy here
			// left products.liquid as "insufficient" → allow_read thrash →
			// "another pass couldn't finish" without propose_changes.
			if strings.HasPrefix(low, "pages/") && strings.HasSuffix(low, ".liquid") &&
				!strings.HasPrefix(low, "pages/css/") {
				return true
			}
			if strings.HasSuffix(low, "pages.json") || low == "pages.json" {
				return true
			}
			if low == "defaults.json" {
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
	fullHome := isFullHomePageRedesignPrompt(prompt)
	sliderFeature := isSliderFeaturePrompt(prompt)
	sliderImagesOnly := isSliderImagesOnlyPrompt(prompt)
	sectionRedesign := isSectionRedesignPrompt(prompt)
	brandScrub := isBrandScrubOrLegalRewritePrompt(prompt)
	bulkDelete := isBulkPageDeletePrompt(prompt)
	blogMeta := isBlogOrMetaRewritePrompt(prompt)
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
		if bulkDelete && !homeRedesign && !sliderFeature && !sectionRedesign {
			switch {
			case base == "pages.json":
				score = 500
			case strings.Contains(low, "pages/blog") && strings.HasSuffix(low, ".liquid"):
				score = 200
			case strings.HasPrefix(low, "pages/") && strings.HasSuffix(low, ".liquid") && !strings.HasPrefix(low, "pages/css/") && !strings.HasPrefix(low, "pages/auth/"):
				score = 160
			default:
				score = 0
			}
		} else if blogMeta && !homeRedesign && !sliderFeature && !sectionRedesign {
			switch {
			case strings.Contains(low, "pages/blog") && strings.HasSuffix(low, ".liquid"):
				score = 450
			case strings.Contains(low, "blog") && strings.Contains(low, "pages/css/") && strings.HasSuffix(low, ".css"):
				score = 430
			case base == "pages.json":
				score = 420
			default:
				score = 0
			}
		} else if brandScrub && !homeRedesign && !sliderFeature && !sectionRedesign {
			named := promptNamedPageSlug(prompt)
			if named != "" && promptScopesToNamedPage(prompt) {
				switch {
				case strings.Contains(low, "pages/"+named) && strings.HasSuffix(low, ".liquid"):
					score = 420
				case strings.Contains(low, named) && strings.Contains(low, "pages/css/") && strings.HasSuffix(low, ".css"):
					score = 410
				default:
					score = 0
				}
			} else {
				switch {
				case strings.Contains(low, "pages/privacy") && strings.HasSuffix(low, ".liquid"):
					score = 420
				case strings.Contains(low, "privacy") && strings.HasSuffix(low, ".css"):
					score = 410
				case strings.Contains(low, "pages/terms") && strings.HasSuffix(low, ".liquid"):
					score = 380
				case strings.Contains(low, "pages/cookie") && strings.HasSuffix(low, ".liquid"):
					score = 370
				case strings.Contains(low, "footer") && strings.HasSuffix(low, ".liquid"):
					score = 360
				case strings.Contains(low, "footer") && strings.HasSuffix(low, ".css"):
					score = 350
				case strings.Contains(low, "header") && strings.HasSuffix(low, ".liquid"):
					score = 340
				case base == "defaults.json":
					score = 330
				default:
					if score > 0 && score < 200 {
						score = 20 // keep SEO pages out of the package
					}
				}
			}
		} else if sectionRedesign && !homeRedesign && !sliderFeature {
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
		} else if homeRedesign || fullHome {
			switch {
			case strings.Contains(low, "pages/home") && strings.HasSuffix(low, ".liquid"):
				score = 320
			case strings.Contains(low, "store-hero-banner") &&
				(strings.HasSuffix(low, ".liquid") || strings.HasSuffix(low, ".css") || strings.HasSuffix(low, ".js")):
				// Canonical hero used by live themes — prefer over obsolete hero-slider.
				score = 310
			case strings.Contains(low, "hero-slider") &&
				(strings.HasSuffix(low, ".liquid") || strings.HasSuffix(low, ".css") || strings.HasSuffix(low, ".js")):
				// Obsolete duplicate — keep low so it rarely enters the package.
				score = 40
			case (strings.Contains(low, "hero") || strings.Contains(low, "slider") || strings.Contains(low, "carousel")) &&
				(strings.HasSuffix(low, ".liquid") || strings.HasSuffix(low, ".css") || strings.HasSuffix(low, ".js")):
				score = 280
			case strings.Contains(low, "sections/") && strings.Contains(low, "home"):
				score = 250
			case strings.Contains(low, "testimonial") && (strings.HasSuffix(low, ".liquid") || strings.HasSuffix(low, ".css") || strings.HasSuffix(low, ".js")):
				score = 230
			case strings.Contains(low, "home") && (strings.HasSuffix(low, ".css") || strings.HasSuffix(low, ".js") || strings.HasSuffix(low, ".liquid")) &&
				!strings.Contains(low, "header") && !strings.Contains(low, "menu") && !strings.Contains(low, "nav"):
				score = 240
			case fullHome && strings.Contains(low, "footer") && (strings.HasSuffix(low, ".liquid") || strings.HasSuffix(low, ".css")):
				score = 220
			case strings.Contains(low, "layout-end") && strings.HasSuffix(low, ".liquid"):
				score = 210
			case base == "pages.json":
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
				if score > 0 && (strings.Contains(low, "header") || strings.Contains(low, "menu")) {
					score = 0
				}
			}
		} else if !wantsNav {
			if strings.Contains(low, "header") || strings.Contains(low, "nav") || strings.Contains(low, "menu") {
				if score > 110 {
					score = 110
				}
			}
		}
		// Slider-only packaging — never demote pages/home for a full homepage rebuild.
		if !fullHome && sliderImagesOnly {
			if strings.Contains(low, "store-hero-banner") && strings.HasSuffix(low, ".liquid") {
				score = 400
			} else {
				score = 0
			}
		} else if !fullHome && sliderFeature {
			if strings.Contains(low, "store-hero-banner") && strings.HasSuffix(low, ".js") {
				score = 310
			} else if strings.Contains(low, "store-hero-banner") && strings.HasSuffix(low, ".liquid") {
				score = 305
			} else if strings.Contains(low, "store-hero-banner") && strings.HasSuffix(low, ".css") {
				score = 295
			} else if strings.Contains(low, "layout-end") && strings.HasSuffix(low, ".liquid") {
				score = 290
			} else if base == "testimonials.js" {
				score = 270
			} else if strings.Contains(low, "pages/home") {
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
		if len(out) >= maxComplexHomePaths {
			break
		}
	}
	return out
}

// ensureCanonicalHomePaths guarantees pages/home.liquid and the live hero
// (store-hero-banner.*) are in the package when they exist on disk, and
// drops obsolete hero-slider.* when store-hero-banner is available.
func ensureCanonicalHomePaths(allPaths, ranked []string, pathCap int) []string {
	onDisk := make(map[string]bool, len(allPaths))
	for _, p := range allPaths {
		onDisk[p] = true
	}
	hasStoreHero := onDisk["components/store-hero-banner.liquid"]
	must := []string{
		"pages/home.liquid",
		"pages/css/home.css",
		"components/store-hero-banner.liquid",
		"components/css/store-hero-banner.css",
		"js/store-hero-banner.js",
	}

	out := make([]string, 0, pathCap)
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] || !onDisk[p] || len(out) >= pathCap {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, p := range must {
		add(p)
	}
	for _, p := range ranked {
		low := strings.ToLower(p)
		if hasStoreHero && strings.Contains(low, "hero-slider") {
			continue
		}
		add(p)
	}
	return out
}

func ensureBrandScrubPaths(allPaths, ranked []string, pathCap int) []string {
	onDisk := make(map[string]bool, len(allPaths))
	for _, p := range allPaths {
		onDisk[p] = true
	}
	must := []string{
		"pages/privacy.liquid",
		"pages/css/privacy.css",
		"components/footer.liquid",
		"components/header.liquid",
		"defaults.json",
		"pages/terms.liquid",
		"pages/cookie.liquid",
	}
	out := make([]string, 0, pathCap)
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] || !onDisk[p] || len(out) >= pathCap {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, p := range must {
		add(p)
	}
	for _, p := range ranked {
		add(p)
	}
	return out
}

func ensureBulkDeletePaths(allPaths, ranked []string, pathCap int) []string {
	onDisk := make(map[string]bool, len(allPaths))
	for _, p := range allPaths {
		onDisk[p] = true
	}
	out := make([]string, 0, pathCap)
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] || !onDisk[p] || len(out) >= pathCap {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	add("pages.json")
	for _, p := range allPaths {
		low := strings.ToLower(p)
		if strings.Contains(low, "pages/blog") && strings.HasSuffix(low, ".liquid") {
			add(p)
		}
	}
	for _, p := range ranked {
		add(p)
	}
	return out
}

func ensureMultiPageCreatePaths(allPaths, ranked []string, pathCap int) []string {
	onDisk := make(map[string]bool, len(allPaths))
	for _, p := range allPaths {
		onDisk[p] = true
	}
	out := make([]string, 0, pathCap)
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] || !onDisk[p] || len(out) >= pathCap {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	// pages.json only — packing blog.liquid/home.liquid caused the model to
	// full-rewrite the index (−1800 lines) instead of creating new pages.
	add("pages.json")
	for _, p := range ranked {
		low := strings.ToLower(p)
		if low == "pages/blog.liquid" || low == "pages/css/blog.css" ||
			low == "pages/home.liquid" || low == "pages/css/home.css" {
			continue
		}
		add(p)
	}
	return out
}

// ensureBlogMetaPaths focuses the package on blog listing + pages.json
// (meta titles) — not the entire theme.
func ensureBlogMetaPaths(allPaths, ranked []string, pathCap int) []string {
	onDisk := make(map[string]bool, len(allPaths))
	for _, p := range allPaths {
		onDisk[p] = true
	}
	out := make([]string, 0, pathCap)
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] || !onDisk[p] || len(out) >= pathCap {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	add("pages/blog.liquid")
	add("pages/css/blog.css")
	add("pages.json")
	for _, p := range allPaths {
		low := strings.ToLower(p)
		if strings.HasPrefix(low, "pages/blog") && (strings.HasSuffix(low, ".liquid") || strings.HasSuffix(low, ".css")) {
			add(p)
		}
	}
	for _, p := range ranked {
		add(p)
	}
	return out
}

func ensureAddToMenuPaths(allPaths, ranked []string, pathCap int) []string {
	onDisk := make(map[string]bool, len(allPaths))
	for _, p := range allPaths {
		onDisk[p] = true
	}
	out := make([]string, 0, pathCap)
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] || !onDisk[p] || len(out) >= pathCap {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	add("defaults.json")
	add("components/header-menu.liquid")
	add("components/header.liquid")
	for _, p := range ranked {
		add(p)
	}
	return out
}

func ensureNamedPagePaths(allPaths, ranked []string, slug string, pathCap int) []string {
	onDisk := make(map[string]bool, len(allPaths))
	for _, p := range allPaths {
		onDisk[p] = true
	}
	must := []string{
		"pages/" + slug + ".liquid",
		"pages/css/" + slug + ".css",
	}
	// Shop route /shop uses pages/products.liquid but layout-start historically
	// links pages/css/product-list.css — pack BOTH so CSS "not applying" fixes
	// land on the file the storefront actually loads.
	if slug == "products" {
		must = append(must, "pages/css/product-list.css")
	}
	// Common alt filenames for about/contact.
	if slug == "about-us" {
		must = append(must, "pages/about.liquid", "pages/css/about.css")
	}
	if slug == "contact-us" {
		must = append(must, "pages/contact.liquid", "pages/css/contact-us.css", "pages/css/contact.css")
	}
	out := make([]string, 0, pathCap)
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] || !onDisk[p] || len(out) >= pathCap {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, p := range must {
		add(p)
	}
	for _, p := range ranked {
		low := strings.ToLower(p)
		if strings.Contains(low, slug) || (slug == "home" && strings.Contains(low, "pages/home")) {
			add(p)
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

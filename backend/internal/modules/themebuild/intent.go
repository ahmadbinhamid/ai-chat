package themebuild

import (
	"regexp"
	"strings"
	"unicode"
)

// Intent is the local CPU-only classification of a merchant prompt before
// any theme load / DeepSeek call. Ambiguous prompts fall through to a
// theme path rather than being treated as conversation.
type Intent string

const (
	IntentConversation  Intent = "conversation"
	IntentSimpleEdit    Intent = "simple_edit"
	IntentMultiFileEdit Intent = "multi_file_edit"
	IntentComplexPage   Intent = "complex_page"
	IntentRepair        Intent = "repair"
	IntentThemeQuery    Intent = "theme_query" // read-only theme question → still needs workspace/AI
)

// Route names for structured logs.
const (
	RouteFastConversation = "fast_conversation"
	RouteLocalFirst       = "local_first"
)

var (
	// conversationOnly matches short, non-theme social/noise turns.
	// Kept as patterns (not a huge phrase list): greetings, thanks, affirmations,
	// and keyboard-mash / filler.
	conversationExact = map[string]bool{
		"hi": true, "hello": true, "hey": true, "hiya": true, "yo": true,
		"sup": true, "hi there": true, "hello there": true, "hey there": true,
		"good morning": true, "good afternoon": true, "good evening": true,
		"thanks": true, "thank you": true, "thx": true, "ty": true,
		"ok": true, "okay": true, "k": true, "kk": true, "cool": true,
		"great": true, "nice": true, "got it": true, "sounds good": true,
		"bye": true, "goodbye": true, "see you": true,
		"test": true, "testing": true, "ping": true, "asdf": true,
		"qwerty": true, "hmm": true, "hmmm": true, "lol": true,
		"yes": true, "yep": true, "yeah": true, "no": true, "nope": true,
		"hi?": true, "hello?": true, "hey?": true,
	}
	conversationPrefix = regexp.MustCompile(`(?i)^(hi|hello|hey|hiya|yo)\b[\s!?.🥰👋]*$`)
	thanksOnly         = regexp.MustCompile(`(?i)^(thanks|thank you|thx|ty)[\s!?.]*$`)
	noiseOnly          = regexp.MustCompile(`(?i)^[a-z]{1,8}$`) // short single token like asdf/qwerty handled via exact map too

	themeTargetRe = regexp.MustCompile(`(?i)\b(header|footer|nav|navbar|menu|hero|slider|carousel|button|banner|logo|cart|sidebar|homepage|home\s*page|product\s*card|section|page|theme|layout|css|liquid|component|partial)\b`)
	themeActionRe = regexp.MustCompile(`(?i)\b(change|update|edit|redesign|restyle|improve|fix|make|tweak|adjust|modify|add|remove|create|build|rewrite|replace|move|resize|recolor|colour|color|style|use)\b`)
	themeQueryRe  = regexp.MustCompile(`(?i)\b(what|where|which|show|find|list|read|explain|describe|how|does|is there|look at)\b`)
	repairRe      = regexp.MustCompile(`(?i)\b(fix|broken|error|bug|crash|not working|doesn'?t work|render failed|blank|missing)\b`)
	complexRe     = regexp.MustCompile(`(?i)\b(from scratch|entire theme|whole site|all pages|every page|new theme|brand kit|redesign (the )?site|multi[- ]page)\b`)
	multiFileRe   = regexp.MustCompile(`(?i)\b(and also|as well as|both .+ and|homepage and|header and footer|all components|several files|multiple files)\b`)

	// pageCreateRe matches NEW PAGE / route creation. Checked BEFORE
	// simple_edit so "create a Contact Us page" never enters the 8k one-shot.
	// Kept narrow: "add a section" / "change the header" must stay simple_edit.
	pageCreateRe = regexp.MustCompile(`(?i)(?:` +
		`\b(?:create|make|build)\s+(?:a\s+|an\s+|the\s+|new\s+)?(?:` +
		`landing\s*page|contact(?:\s*us)?(?:\s*page)?|about(?:\s*page)?|faq(?:\s*page)?|page` +
		`)\b` +
		`|` +
		`\badd\s+(?:a\s+|an\s+|the\s+)?(?:new\s+)?(?:` +
		`landing\s*page|(?:faq|about|contact(?:\s*us)?)\s*page|page` +
		`)\b` +
		`|` +
		`\b(?:new)\s+(?:landing\s*page|page|route)\b` +
		`|` +
		// Roman-Urdu / free word order: "page create …", "ek page create kr …"
		`\bpage\b[\s\S]{0,24}\bcreate\b` +
		`)`)

	// pageAndMenuRe: page + menu/nav together with a create/add/new action —
	// structural multi-file work (register page + wire navigation).
	pageAndMenuRe = regexp.MustCompile(`(?i)\b(?:create|make|build|add|new)\b`)
	menuNavRe     = regexp.MustCompile(`(?i)\b(?:menu|navigation|navbar)\b|\bnav\b`)
	addToMenuRe   = regexp.MustCompile(`(?i)\b(?:add|put|include)\b[\s\S]{0,48}\b(?:to\s+)?(?:the\s+)?(?:menu|navigation|navbar)\b`)
	pageLikeNameRe = regexp.MustCompile(`(?i)\b(?:contact|about|faq|landing)\b`)

	// pageRedesignRe matches substantial homepage/page redesigns (slider,
	// full design overhaul) that must NOT use the 8k simple_edit one-shot.
	// Narrow: "change homepage hero color" / "update homepage hero" stay simple.
	pageRedesignRe = regexp.MustCompile(`(?i)(?:` +
		`\b(?:redesign|restyle|rewrite|overhaul)\b[\s\S]{0,48}\b(?:home\s*page|homepage|landing(?:\s*page)?)\b` +
		`|` +
		`\b(?:home\s*page|homepage|landing(?:\s*page)?)\b[\s\S]{0,48}\b(?:redesign|restyle|rewrite|overhaul)\b` +
		`|` +
		`\b(?:change|update|make|improve|edit)\b[\s\S]{0,72}\b(?:home\s*page|homepage)\b[\s\S]{0,72}\b(?:design|desgin|slider|carousel|beautiful|beautifull|layout)\b` +
		`|` +
		`\b(?:home\s*page|homepage)\b[\s\S]{0,48}\b(?:slider|carousel)\b` +
		`|` +
		`\b(?:beautiful|beautifull|proper)\b[\s\S]{0,40}\b(?:home\s*page|homepage)\b` +
		`)`)

	// sliderFeatureRe: multi-image / autoplay / auto-scroll on a slider —
	// liquid+js+css work that truncates under the 8k simple_edit ceiling.
	// Typos (imges, scrol, multilpal) are intentional — merchants type them.
	sliderOrCarouselRe = regexp.MustCompile(`(?i)\b(?:slider|carousel)\b`)
	sliderMultiImageRe = regexp.MustCompile(`(?i)(?:` +
		`\b(?:multiple|multi\w*|several|more)\b[\s\S]{0,48}\b(?:images?|imges|imgs?|photos?|pics?)\b` +
		`|` +
		`\b(?:\d+)\s+(?:\w+[\s/_-]*){0,8}(?:images?|imges|imgs?|photos?|pics?|slides?)\b` +
		`|` +
		`\b(?:images?|imges|imgs?|photos?|pics?)\b[\s\S]{0,40}\b(?:on|in|for)\b[\s\S]{0,24}\b(?:slider|carousel)\b` +
		`)`)
	sliderAutoplayRe = regexp.MustCompile(`(?i)\bauto[\s-]*(?:scroll|scrol|play)\b|\bautoplay\b`)
	// Merchant complaining the slider is broken / only shows static images —
	// must stay on complex_page repair, never simple_edit that deletes slides.
	sliderBrokenRe = regexp.MustCompile(`(?i)(?:` +
		`\b(?:not working|isn'?t working|broken|doesn'?t work|fix)\b` +
		`|` +
		`\b(?:nahi|ni)\b` +
		`|` +
		`\b(?:chal\s*r?a?h[ei]?\s*n|nhi\s*chal)\b` +
		`|` +
		`\b(?:only |just |to )?(?:images?|iamges|pics?)\b` +
		`)`)
	// Image-swap only: keep autoplay wiring, just replace slide <img> src URLs.
	// Window is 96 chars so "hero slider … 5 different … Unsplash images" still matches.
	sliderImagesOnlyRe = regexp.MustCompile(`(?i)(?:` +
		`\b(?:natural|real|stock|photo|public\s*url|https?|unsplash)\b[\s\S]{0,96}\b(?:images?|imges|pics?|photos?)\b` +
		`|` +
		`\b(?:images?|imges|pics?|photos?)\b[\s\S]{0,96}\b(?:slider|carousel|hero)\b` +
		`|` +
		`\b(?:slider|carousel|hero)\b[\s\S]{0,96}\b(?:images?|imges|pics?|photos?)\b` +
		`|` +
		`\b(?:\d+)\s*(?:pics?|images?|imges|photos?|slides?)\b` +
		`|` +
		`\b(?:\d+)\s+(?:\w+[\s/_-]*){0,8}(?:pics?|images?|imges|photos?|slides?)\b` +
		`)`)
)

// ClassifyIntent is a deterministic local router. Attachments / non-edit modes
// never classify as conversation. Ambiguous text defaults toward a theme path.
//
// Attachments (uploaded files OR a prompt ReferenceURL) only suppress the
// conversation short-circuit — they must NOT skip page-create / slider
// structural routing. An Unsplash "reference image" URL used to force
// simple_edit and then fail the 4k patch guard on a 5-slide hero rewrite.
func ClassifyIntent(prompt, mode string, hasAttachments bool) Intent {
	if mode != "" && mode != "edit" {
		switch mode {
		case "brand", "copy":
			return IntentSimpleEdit
		case "pages":
			return IntentComplexPage
		default:
			return IntentSimpleEdit
		}
	}

	p := strings.ToLower(strings.TrimSpace(prompt))
	p = strings.Join(strings.Fields(p), " ")
	if p == "" {
		if hasAttachments {
			return IntentSimpleEdit
		}
		return IntentConversation
	}

	// Conversation only when there is no attachment — a bare "hi" with a
	// file still means theme work, not a greeting.
	if !hasAttachments {
		normalized := strings.TrimRight(p, "!?.,;: ")
		if conversationExact[p] || conversationExact[normalized] {
			return IntentConversation
		}
		if conversationPrefix.MatchString(p) || thanksOnly.MatchString(p) {
			return IntentConversation
		}
		// Pure punctuation / emoji-ish short noise with no theme words.
		if !themeTargetRe.MatchString(p) && !themeActionRe.MatchString(p) && !themeQueryRe.MatchString(p) && !repairRe.MatchString(p) {
			if len([]rune(p)) <= 12 && mostlyNonThemeNoise(p) {
				return IntentConversation
			}
			if noiseOnly.MatchString(normalized) && len(normalized) <= 8 && !looksLikeThemeWord(normalized) {
				return IntentConversation
			}
		}
	}

	if complexRe.MatchString(p) {
		return IntentComplexPage
	}
	// Page create / redesign / page+menu / slider MUST run before simple_edit
	// matching — including when a ReferenceURL made hasAttachments=true.
	if isPageCreateOrStructural(p) {
		return IntentComplexPage
	}
	if repairRe.MatchString(p) && (themeTargetRe.MatchString(p) || strings.Contains(p, "render") || strings.Contains(p, "liquid") || strings.Contains(p, "error")) {
		return IntentRepair
	}
	// Theme questions need workspace/AI — not conversation.
	if themeQueryRe.MatchString(p) && themeTargetRe.MatchString(p) {
		return IntentThemeQuery
	}
	if themeQueryRe.MatchString(p) && (strings.Contains(p, "file") || strings.Contains(p, "theme") || strings.Contains(p, "page")) {
		return IntentThemeQuery
	}
	if multiFileRe.MatchString(p) && themeActionRe.MatchString(p) {
		return IntentMultiFileEdit
	}
	if themeTargetRe.MatchString(p) && themeActionRe.MatchString(p) {
		return IntentSimpleEdit
	}
	if themeActionRe.MatchString(p) && (strings.Contains(p, "page") || strings.Contains(p, "theme") || strings.Contains(p, "section")) {
		return IntentSimpleEdit
	}

	// Ambiguous → theme path (safer than wrong conversation classification).
	return IntentSimpleEdit
}

// sectionRedesignRe: substantial header/footer rebuilds (newsletter, link
// columns, SaaS chrome) — never the 8k simple_edit one-shot. Tiny tweaks
// ("make the footer text white" / "make the header modern") stay simple_edit.
var sectionRedesignRe = regexp.MustCompile(`(?i)(?:` +
	`\b(?:redesign|restyle|rewrite|overhaul|rebuild)\b[\s\S]{0,64}\b(?:footer|header)\b` +
	`|` +
	`\b(?:footer|header)\b[\s\S]{0,64}\b(?:redesign|restyle|rewrite|overhaul|rebuild)\b` +
	`|` +
	`\b(?:footer|header)\b[\s\S]{0,220}\b(?:newsletter|saas|social\s*media|copyright|privacy\s*policy|terms\s*(?:&|and)?\s*conditions|cookie\s*policy|documentation|link\s*columns?|multi[- ]column)\b` +
	`)`)

// isPageCreateOrStructural reports new-page / page redesign / page+menu work
// that must use the full multi-file generation path, never SIMPLE_EDIT one-shot.
func isPageCreateOrStructural(p string) bool {
	if pageCreateRe.MatchString(p) {
		return true
	}
	if pageRedesignRe.MatchString(p) {
		return true
	}
	if isSliderFeaturePrompt(p) {
		return true
	}
	if isSectionRedesignPrompt(p) {
		return true
	}
	// "… page … menu …" (or nav) with create/add/new — e.g. create page and add to menu.
	hasPage := strings.Contains(p, "page")
	hasMenu := menuNavRe.MatchString(p)
	if hasPage && hasMenu && pageAndMenuRe.MatchString(p) {
		return true
	}
	// Explicit "add … to (the) menu/navigation" with a page-like object.
	if hasMenu && addToMenuRe.MatchString(p) {
		if hasPage || pageLikeNameRe.MatchString(p) {
			return true
		}
	}
	return false
}

// isSectionRedesignPrompt is a full header/footer rebuild (multi-column SaaS
// chrome, newsletter, legal bar) — liquid+css that truncates under simple_edit.
func isSectionRedesignPrompt(p string) bool {
	p = strings.ToLower(strings.TrimSpace(p))
	if !(strings.Contains(p, "footer") || strings.Contains(p, "header")) {
		return false
	}
	return sectionRedesignRe.MatchString(p)
}

// isSliderFeaturePrompt is multi-image / autoplay / broken-slider / image-swap
// work on a slider/carousel — never the 8k simple_edit one-shot.
func isSliderFeaturePrompt(p string) bool {
	if isSliderImagesOnlyPrompt(p) {
		return true
	}
	if !sliderOrCarouselRe.MatchString(p) {
		return false
	}
	return sliderMultiImageRe.MatchString(p) || sliderAutoplayRe.MatchString(p) || sliderBrokenRe.MatchString(p)
}

// isSliderImagesOnlyPrompt is a narrow swap of slide <img> src URLs — keep
// existing JS/CSS/layout wiring, only rewrite liquid image sources.
func isSliderImagesOnlyPrompt(p string) bool {
	p = strings.ToLower(strings.TrimSpace(p))
	if !(strings.Contains(p, "slider") || strings.Contains(p, "carousel") || strings.Contains(p, "hero")) {
		return false
	}
	if !sliderImagesOnlyRe.MatchString(p) {
		return false
	}
	// Strong URL/photo-swap cues only — bare "images" alone is not enough
	// (that also appears in "multiple images + autoplay" rebuilds).
	urlSwapCue := strings.Contains(p, "natural") || strings.Contains(p, "stock") ||
		strings.Contains(p, "public") || strings.Contains(p, "picsum") ||
		strings.Contains(p, "unsplash") || strings.Contains(p, "http") ||
		strings.Contains(p, "photo") || strings.Contains(p, "url")
	if !urlSwapCue {
		return false
	}
	// Autoplay / working-slider rebuilds need full JS+CSS wiring even when
	// the merchant also mentions photos/URLs.
	if sliderAutoplayRe.MatchString(p) || strings.Contains(p, "working") ||
		strings.Contains(p, "autoplay") || strings.Contains(p, "broken") ||
		strings.Contains(p, "not working") {
		return false
	}
	return true
}

// intentUsesSimpleEditOneShot is the single gate for the narrow one-shot
// path — kept here so route selection cannot silently diverge from intent.
func intentUsesSimpleEditOneShot(intent Intent) bool {
	return intent == IntentSimpleEdit
}

func mostlyNonThemeNoise(p string) bool {
	letters := 0
	for _, r := range p {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	return letters <= 8
}

func looksLikeThemeWord(s string) bool {
	switch s {
	case "header", "footer", "hero", "slider", "carousel", "nav", "menu", "logo", "cart", "css", "js",
		"page", "home", "theme", "fix", "edit", "make", "add":
		return true
	default:
		return false
	}
}

// conversationReply picks a short merchant-facing reply without calling DeepSeek.
func conversationReply(prompt string) string {
	p := strings.ToLower(strings.TrimSpace(prompt))
	p = strings.Join(strings.Fields(p), " ")
	switch {
	case thanksOnly.MatchString(p) || strings.HasPrefix(p, "thank"):
		return "You're welcome! Tell me what you'd like to create or change next."
	case conversationExact[p] || conversationPrefix.MatchString(p) ||
		strings.HasPrefix(p, "hi") || strings.HasPrefix(p, "hello") || strings.HasPrefix(p, "hey"):
		return "Hi! I'm ready to help. What would you like to create or change?"
	case p == "bye" || p == "goodbye" || strings.HasPrefix(p, "see you"):
		return "Goodbye! Come back anytime you want to edit your theme."
	default:
		return "I'm ready when you are — describe a change (for example the header, a button, or a page) and I'll get started."
	}
}

// intentUsesThemePipeline reports whether ClassifyIntent requires workspace/AI.
func intentUsesThemePipeline(intent Intent) bool {
	return intent != IntentConversation
}

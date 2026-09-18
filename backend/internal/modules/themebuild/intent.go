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

	// brandScrubRe: merchant wants old brand (JPRO / store name) gone across
	// pages/files — Roman Urdu ("kisi b page", "sari files/fies", "jpro ni")
	// and English. Must NOT use the 2-iter simple_edit one-shot (model greps
	// forever and dies with "another pass couldn't finish").
	brandScrubRe = regexp.MustCompile(`(?i)(?:` +
		`\b(?:no|remove|strip|delete|clear)\b[\s\S]{0,48}\bjpro\b` +
		`|` +
		`\bjpro\b[\s\S]{0,64}\b(?:ni|nahi|nhi|not|dont|don't|any\s+page|every\s+page|all\s+pages?)\b` +
		`|` +
		`\b(?:kisi|kissi)\s*b(?:hi)?\s*page\b` +
		`|` +
		`\b(?:sari|saari|sab)\s*(?:files?|fies|pages?)\b` +
		`|` +
		`\b(?:all|every|entire)\s+(?:theme\s+)?files?\b` +
		`|` +
		`\b(?:across|throughout)\b[\s\S]{0,32}\b(?:theme|pages?|site)\b` +
		`|` +
		`\b(?:rebrand|brand\s*scrub|remove\s+branding)\b` +
		`)`)

	// legalPageRewriteRe: privacy/terms/cookie content rewrite (often paired
	// with "software company") — large liquid bodies, not an 8k one-shot.
	legalPageRewriteRe = regexp.MustCompile(`(?i)(?:` +
		`\b(?:update|rewrite|replace|change|edit)\b[\s\S]{0,80}\b(?:privacy|terms|cookie)\b` +
		`|` +
		`\b(?:privacy|terms|cookie)\b[\s\S]{0,64}\b(?:page|policy|content|contnent)\b[\s\S]{0,80}\b(?:update|rewrite|software|company|saas)\b` +
		`|` +
		`\b(?:privacy|terms|cookie)\b[\s\S]{0,48}\b(?:software\s*company|saas|according\s+to)\b` +
		`)`)

	// pageCreateRe matches NEW PAGE / route creation. Checked BEFORE
	// simple_edit so "create a Contact Us page" never enters the 8k one-shot.
	// Kept narrow: "add a section" / "change the header" must stay simple_edit.
	pageCreateRe = regexp.MustCompile(`(?i)(?:` +
		`\b(?:create|make|build|generate|genrate)\s+(?:a\s+|an\s+|the\s+|new\s+)?(?:` +
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
		`\bpage\b[\s\S]{0,24}\b(?:create|generate|genrate)\b` +
		`|` +
		// "10 blog pages" / "generate 10 blog pages" (digit required — not "add pages.json")
		`\b(?:\d{1,2})\s+blogs?\s*pages?\b` +
		`|` +
		`\b(?:create|make|build|generate|genrate|add)\b[\s\S]{0,48}\b\d{1,2}\s+(?:blog\s*)?pages?\b` +
		`|` +
		`\b(?:create|make|build|generate|genrate)\b[\s\S]{0,48}\bblog\s*pages?\b` +
		`)`)

	// multiPageCreateCountRe extracts how many new pages the merchant wants.
	multiPageCreateCountRe = regexp.MustCompile(`(?i)\b(\d{1,2})\s+(?:blog\s*)?pages?\b|\b(\d{1,2})\s+blogs?\b`)

	// pageAndMenuRe: page + menu/nav together with a create/add/new action —
	// structural multi-file work (register page + wire navigation).
	pageAndMenuRe = regexp.MustCompile(`(?i)\b(?:create|make|build|add|new)\b`)
	menuNavRe     = regexp.MustCompile(`(?i)\b(?:menu|navigation|navbar)\b|\bnav\b`)
	addToMenuRe   = regexp.MustCompile(`(?i)\b(?:add|put|include)\b[\s\S]{0,48}\b(?:to\s+|in\s+)?(?:the\s+)?(?:menu|navigation|navbar)\b`)
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
		`|` +
		// Merchant: homepage wrong / looks like plain HTML / CSS missing —
		// must rebuild liquid+css together, never a one-file ±0 tweak.
		`\b(?:home\s*page|homepage)\b[\s\S]{0,96}\b(?:not\s+correct|incorrect|wrong|broken|only\s+(?:like\s+)?html|like\s+html|plain\s+html|no\s+css|without\s+css|css\s*(?:not|ni|nahi)|desgin|design)\b` +
		`|` +
		`\b(?:only\s+(?:like\s+)?html|like\s+html|plain\s+html|not\s+any\s+css|no\s+css\s+apply)\b[\s\S]{0,64}\b(?:home\s*page|homepage|home)\b` +
		`)`)

	// changeNotVisibleRe: merchant says the last edit still looks the same
	// (English + Roman Urdu). Must not fall through to "couldn't understand".
	changeNotVisibleRe = regexp.MustCompile(`(?i)(?:` +
		`\bstill\s+(?:looks?|looking|the\s+same|same|showing)\b` +
		`|` +
		`\b(?:looks?|looking)\s+(?:the\s+)?same\b` +
		`|` +
		`\bno\s+change\b|\bnothing\s+changed\b|\bdidn'?t\s+change\b` +
		`|` +
		`\b(?:abi|abhi)\s*b?h?i?\b[\s\S]{0,32}\b(?:wasy|waisi|wese|waisa|same)\b` +
		`|` +
		`\b(?:wasy|waisi|wese|waisa)\s*e?\b[\s\S]{0,24}\b(?:dek|dekh)\b` +
		`|` +
		`\bsame\s+(?:dikha?|dekh|look|page)\b` +
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

	if isPagesListPrompt(p) {
		return IntentThemeQuery
	}
	if isBulkPageDeletePrompt(p) {
		return IntentComplexPage
	}
	if complexRe.MatchString(p) {
		return IntentComplexPage
	}
	// Theme-wide brand scrub / privacy-legal rewrite — never simple_edit.
	if isBrandScrubOrLegalRewritePrompt(p) {
		return IntentComplexPage
	}
	// Substantial page content rewrite/fill (e.g. "services page pe zada
	// content add kro") — never the 8k simple_edit one-shot (that rejects
	// large updates and often targets the wrong file).
	if isPageContentRewritePrompt(p) {
		return IntentComplexPage
	}
	// Page create / redesign / page+menu / slider MUST run before simple_edit
	// matching — including when a ReferenceURL made hasAttachments=true.
	if isPageCreateOrStructural(p) {
		return IntentComplexPage
	}
	if isHomeCSSBrokenPrompt(p) {
		return IntentComplexPage
	}
	if isNamedPageCSSBrokenPrompt(p) {
		return IntentComplexPage
	}
	// Reference URL / "make homepage same as this" (incl. Roman Urdu
	// "asa chy" / "same kro") — never simple_edit typo/card tweaks.
	if isHomeReferenceClonePrompt(p) {
		return IntentComplexPage
	}
	// "Still looks the same" / Roman Urdu "abi b wasy e dek rha" — previous
	// edit didn't show. Route to repair (not conversation / empty clarify).
	if changeNotVisibleRe.MatchString(p) {
		return IntentRepair
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
// ("make the footer text white" / "make copyright lighter") stay simple_edit.
// Do NOT treat bare "copyright" alone as a redesign cue.
var sectionRedesignRe = regexp.MustCompile(`(?i)(?:` +
	`\b(?:redesign|restyle|rewrite|overhaul|rebuild)\b[\s\S]{0,64}\b(?:footer|header)\b` +
	`|` +
	`\b(?:footer|header)\b[\s\S]{0,64}\b(?:redesign|restyle|rewrite|overhaul|rebuild)\b` +
	`|` +
	`\b(?:footer|header)\b[\s\S]{0,220}\b(?:newsletter|saas|social\s*media|privacy\s*policy|terms\s*(?:&|and)?\s*conditions|cookie\s*policy|documentation|link\s*columns?|multi[- ]column|bottom\s*bar)\b` +
	`|` +
	`\b(?:footer|header)\b[\s\S]{0,220}\bcopyright\b[\s\S]{0,48}\b(?:privacy|terms|cookie|newsletter|social)\b` +
	`)`)

// sectionCSSBrokenRe: merchant says footer/header CSS/design did not apply —
// Roman Urdu ("ni apply", "nahi hui") and English. Must not use simple_edit.
var sectionCSSBrokenRe = regexp.MustCompile(`(?i)(?:` +
	`\b(?:not\s+applied|didn'?t\s+apply|doesn'?t\s+apply|won'?t\s+apply|no\s+apply)\b` +
	`|` +
	`\b(?:ni|nahi|nhi)\s*apply\b` +
	`|` +
	`\bapply\b[\s\S]{0,32}\b(?:nahi|ni|nhi|hoi|hui|hua|hne)\b` +
	`|` +
	`\b(?:css|style|design|desgin)\b[\s\S]{0,48}\b(?:not\s+working|broken|missing|nahi|ni|nhi)\b` +
	`|` +
	`\b(?:css|style)\b[\s\S]{0,24}\b(?:e\s+)?(?:ni|nahi|nhi)\b` +
	`)`)

// isBrandScrubOrLegalRewritePrompt is theme-wide "no JPRO on any page" /
// "look at all files" OR a privacy/terms/cookie rewrite for a software
// company — both need complex_page budgets, not simple_edit explore thrash.
func isBrandScrubOrLegalRewritePrompt(p string) bool {
	p = strings.ToLower(strings.Join(strings.Fields(p), " "))
	if brandScrubRe.MatchString(p) {
		return true
	}
	return legalPageRewriteRe.MatchString(p)
}

// isPageCreateOrStructural reports new-page / page redesign / page+menu work
// that must use the full multi-file generation path, never SIMPLE_EDIT one-shot.
func isPageCreateOrStructural(p string) bool {
	if isMultiPageCreatePrompt(p) {
		return true
	}
	if isPageContentRewritePrompt(p) {
		return true
	}
	if isNamedPageCSSBrokenPrompt(p) {
		return true
	}
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
	// Explicit "add … to/in (the) menu/navigation" — Services, FAQ, etc.
	if hasMenu && addToMenuRe.MatchString(p) {
		return true
	}
	return false
}

// isMultiPageCreatePrompt is true when the merchant wants several NEW pages
// created in one turn (e.g. "generate 10 blog pages"). Must use complex_page
// with a pages.json full-body update — never simple_edit on blog.liquid alone.
func isMultiPageCreatePrompt(prompt string) bool {
	if isBulkPageDeletePrompt(prompt) {
		return false
	}
	n := requestedNewPageCount(prompt)
	if n < 2 {
		return false
	}
	p := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
	return pageCreateRe.MatchString(p) ||
		strings.Contains(p, "blog") ||
		strings.Contains(p, "generate") ||
		strings.Contains(p, "genrate") ||
		strings.Contains(p, "create") ||
		strings.Contains(p, "make") ||
		strings.Contains(p, "build") ||
		strings.Contains(p, "add")
}

// requestedNewPageCount returns how many new pages the merchant asked for
// (0 if unclear). Capped at 10 for intent display; the model still only
// creates multiPageCreateBatchSize per turn (see that helper).
func requestedNewPageCount(prompt string) int {
	p := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
	m := multiPageCreateCountRe.FindStringSubmatch(p)
	if len(m) == 0 {
		return 0
	}
	raw := m[1]
	if raw == "" {
		raw = m[2]
	}
	n := 0
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int(ch-'0')
	}
	if n < 2 {
		return 0
	}
	if n > 10 {
		return 10
	}
	return n
}

// multiPageCreateBatchSize is how many pages the AI must create THIS turn.
// Large N (e.g. 10) truncates DeepSeek streams — batch so content stays
// merchant-specific and AI-authored, not a Go template dump.
const maxMultiPageCreateBatch = 3

func multiPageCreateBatchSize(prompt string) int {
	n := requestedNewPageCount(prompt)
	if n <= 0 {
		return 0
	}
	if n > maxMultiPageCreateBatch {
		return maxMultiPageCreateBatch
	}
	return n
}

// isPageContentRewritePrompt is a substantial rewrite/fill of a named page
// ("services page pe software company content add kro", "zada sara content",
// "shop page ko software company theme ke mutabiq regenerate").
// Must use complex_page — simple_edit rejects large patches and often edits
// an unrelated component (card-essentials / contact-inquiry) instead.
func isPageContentRewritePrompt(prompt string) bool {
	p := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
	if isBulkPageDeletePrompt(p) || isAddToMenuPrompt(p) || isMultiPageCreatePrompt(p) {
		return false
	}
	if isBlogOrMetaRewritePrompt(p) {
		return true
	}
	slug := promptNamedPageSlug(p)
	if slug == "" && !strings.Contains(p, "page") && !strings.Contains(p, "blog") {
		return false
	}
	// Named page + regenerate / software-theme redesign (typos intentional).
	if slug != "" && namedPageThemeRegenCue(p) {
		return true
	}
	if slug != "" && isNamedPageStillWrongPrompt(p) {
		return true
	}
	contentCue := strings.Contains(p, "content") || strings.Contains(p, "contnent") ||
		strings.Contains(p, "conent") || // common typo
		strings.Contains(p, "copy") || strings.Contains(p, "rewrite") ||
		strings.Contains(p, "recreate") || strings.Contains(p, "fill") ||
		strings.Contains(p, "zada") || strings.Contains(p, "zyada") ||
		strings.Contains(p, "lots") || strings.Contains(p, "more text") ||
		strings.Contains(p, "software company") || strings.Contains(p, "software house") ||
		strings.Contains(p, "software compan") || // compnay typo
		strings.Contains(p, "saas")
	actionCue := strings.Contains(p, "add") || strings.Contains(p, "update") ||
		strings.Contains(p, "change") || strings.Contains(p, "edit") ||
		strings.Contains(p, "write") || strings.Contains(p, "rewrite") ||
		strings.Contains(p, "recreate") || strings.Contains(p, "make") ||
		strings.Contains(p, "regen") || strings.Contains(p, "redesign") ||
		strings.Contains(p, "kr") || strings.Contains(p, "kar")
	return contentCue && actionCue
}

// isBlogOrMetaRewritePrompt is blog listing / post copy rewrite and/or
// meta-title / SEO scrub (e.g. "change the blogs according to software house
// … still not change the jpro meta titles"). Must use complex_page — never
// multi_file_edit explore thrash with a 45s TTFT budget.
func isBlogOrMetaRewritePrompt(prompt string) bool {
	p := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
	if isMultiPageCreatePrompt(p) || isBulkPageDeletePrompt(p) {
		return false
	}
	hasBlog := strings.Contains(p, "blog")
	hasMeta := strings.Contains(p, "meta") || strings.Contains(p, "seo") ||
		(strings.Contains(p, "title") && (strings.Contains(p, "meta") || strings.Contains(p, "jpro") || strings.Contains(p, "site")))
	if !hasBlog && !hasMeta {
		return false
	}
	topic := strings.Contains(p, "software") || strings.Contains(p, "saas") ||
		strings.Contains(p, "jpro") || strings.Contains(p, "company") || strings.Contains(p, "house")
	action := strings.Contains(p, "change") || strings.Contains(p, "update") ||
		strings.Contains(p, "rewrite") || strings.Contains(p, "edit") ||
		strings.Contains(p, "according") || strings.Contains(p, "still") ||
		strings.Contains(p, "fix") || strings.Contains(p, "from")
	return topic && action
}

// namedPageThemeRegenCue is "regenerate this page for software company theme"
// including Roman Urdu ("mutabiq"/"mutibq") and common typos.
func namedPageThemeRegenCue(p string) bool {
	regen := strings.Contains(p, "regen") || strings.Contains(p, "redesign") ||
		strings.Contains(p, "restyle") || strings.Contains(p, "rewrite") ||
		strings.Contains(p, "recreate") || strings.Contains(p, "overhaul")
	themeSoft := strings.Contains(p, "theme") ||
		strings.Contains(p, "software") ||
		strings.Contains(p, "saas") ||
		strings.Contains(p, "mutabi") || strings.Contains(p, "mutibq")
	return regen && themeSoft ||
		(strings.Contains(p, "software") && (strings.Contains(p, "compan") || strings.Contains(p, "house") || strings.Contains(p, "theme")) &&
			(regen || strings.Contains(p, "kr") || strings.Contains(p, "kar") || strings.Contains(p, "update") || strings.Contains(p, "change")))
}

// isNamedPageStillWrongPrompt is a follow-up that the named page still shows
// old brand/copy ("shop page pe abi b jpro", "tm kuch b change ni kye").
func isNamedPageStillWrongPrompt(prompt string) bool {
	p := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
	if promptNamedPageSlug(p) == "" {
		return false
	}
	still := strings.Contains(p, "abi") || strings.Contains(p, "abhi") ||
		strings.Contains(p, "still") || strings.Contains(p, "again") ||
		strings.Contains(p, "arha") || strings.Contains(p, "aa rha") ||
		strings.Contains(p, "showing")
	wrong := strings.Contains(p, "jpro") || strings.Contains(p, "j pro") ||
		strings.Contains(p, "change ni") || strings.Contains(p, "ni kye") ||
		strings.Contains(p, "nahi") || strings.Contains(p, "nothing") ||
		strings.Contains(p, "same") || strings.Contains(p, "wasy")
	return still && wrong
}

// isSectionRedesignPrompt is a full header/footer rebuild OR a follow-up that
// the section CSS/design did not apply — liquid+css that truncates under simple_edit.
func isSectionRedesignPrompt(p string) bool {
	p = strings.ToLower(strings.TrimSpace(p))
	if !(strings.Contains(p, "footer") || strings.Contains(p, "header")) {
		return false
	}
	if sectionRedesignRe.MatchString(p) {
		return true
	}
	return isSectionCSSBrokenPrompt(p)
}

// isSectionCSSBrokenPrompt is "footer/header css/design not applied" — often
// after a redesign where liquid classes and CSS selectors diverged.
func isSectionCSSBrokenPrompt(p string) bool {
	p = strings.ToLower(strings.TrimSpace(p))
	if !(strings.Contains(p, "footer") || strings.Contains(p, "header")) {
		return false
	}
	if !sectionCSSBrokenRe.MatchString(p) {
		return false
	}
	// Prefer an explicit css/style/design cue, but "footer ni apply" is enough.
	return strings.Contains(p, "css") || strings.Contains(p, "style") ||
		strings.Contains(p, "design") || strings.Contains(p, "desgin") ||
		strings.Contains(p, "apply")
}

// isNamedPageCSSBrokenPrompt is "shop/services/… page CSS not applying" —
// liquid+css resync for the named page, never simple_edit explore thrash.
func isNamedPageCSSBrokenPrompt(prompt string) bool {
	p := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
	slug := promptNamedPageSlug(p)
	if slug == "" && !strings.Contains(p, "page") {
		return false
	}
	if !sectionCSSBrokenRe.MatchString(p) &&
		!(strings.Contains(p, "css") && (strings.Contains(p, "proper") || strings.Contains(p, "fix") || strings.Contains(p, "ni") || strings.Contains(p, "nahi"))) {
		return false
	}
	return slug != "" || strings.Contains(p, "page")
}

// isHomeCSSBrokenPrompt is "homepage looks like plain HTML / CSS not applied" —
// the liquid/CSS class-mismatch failure mode. Must use complex_page full-home
// packaging so both pages/home.liquid and pages/css/home.css are rewritten together.
func isHomeCSSBrokenPrompt(p string) bool {
	p = strings.ToLower(strings.Join(strings.Fields(p), " "))
	hasHome := strings.Contains(p, "homepage") || strings.Contains(p, "home page") ||
		(strings.Contains(p, "home") && (strings.Contains(p, "page") || strings.Contains(p, "design") ||
			strings.Contains(p, "desgin") || strings.Contains(p, "css") || strings.Contains(p, "html")))
	if !hasHome {
		return false
	}
	if strings.Contains(p, "like html") || strings.Contains(p, "only html") ||
		strings.Contains(p, "plain html") || strings.Contains(p, "only like html") ||
		strings.Contains(p, "not any css") || strings.Contains(p, "no css") ||
		strings.Contains(p, "without css") || strings.Contains(p, "not correct") {
		return true
	}
	return sectionCSSBrokenRe.MatchString(p)
}

// homeCloneCueRe: merchant wants homepage to match a reference site / look
// "the same" / Roman Urdu "asa chy" / "same kro" / "proper dikhao".
var homeCloneCueRe = regexp.MustCompile(`(?i)(?:` +
	`\b(?:same\s+kro|same\s+karo|same\s+site|same\s+page|same\s+as|just\s+like|look\s+like|make\s+it\s+like|copy\s+this|clone|match\s+this|inspired\s+by)\b` +
	`|` +
	`\basa\s+chy\b|\basi\s+(?:chy|chahiye|ready)\b|\baisy\s+chy\b|\baisi\s+chy\b` +
	`|` +
	`\bproper\s+(?:diko|dikhao|dikha|show)\b` +
	`|` +
	`\b(?:home\s*page|homepage)\b[\s\S]{0,48}\b(?:same|like|asa|aisi|aisy|clone|match|copy)\b` +
	`|` +
	`\b(?:same|like|asa|aisi|aisy|clone|match|copy)\b[\s\S]{0,48}\b(?:home\s*page|homepage)\b` +
	`)`)

func promptMentionsHome(p string) bool {
	p = strings.ToLower(p)
	return strings.Contains(p, "homepage") || strings.Contains(p, "home page") ||
		(strings.Contains(p, "home") && strings.Contains(p, "page"))
}

func promptHasHTTPURL(p string) bool {
	low := strings.ToLower(p)
	return strings.Contains(low, "http://") || strings.Contains(low, "https://") ||
		strings.Contains(low, "www.")
}

// isHomeReferenceClonePrompt is "make my homepage like this URL / same as
// that site" — must rebuild the whole homepage from the reference, never a
// spelling fix or single card-essentials edit (observed in production).
func isHomeReferenceClonePrompt(p string) bool {
	p = strings.ToLower(strings.Join(strings.Fields(p), " "))
	if !promptMentionsHome(p) {
		return false
	}
	if homeCloneCueRe.MatchString(p) {
		return true
	}
	// URL in the same message as "home page" is almost always "make it like this".
	if promptHasHTTPURL(p) {
		return true
	}
	return false
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

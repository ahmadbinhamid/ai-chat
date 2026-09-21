package builderplan

import (
	"regexp"
	"strconv"
	"strings"
)

// DeterministicClassifier is the default CPU classifier. No model files,
// no GPU, no network — regex/heuristic only. Swap via Classifier interface.
type DeterministicClassifier struct{}

var _ Classifier = DeterministicClassifier{}

var (
	pageCountRe = regexp.MustCompile(`(?i)\b(\d{1,2})\s+(?:blog\s*)?pages?\b|\b(\d{1,2})\s+blogs?\b`)
	pageWordCountRe = regexp.MustCompile(`(?i)\b(pair|couple|both|two|three|four|five)\b[\s\S]{0,24}\b(?:blog\s*)?pages?\b|\b(pair|couple|both|two|three|four|five)\b\s+blogs?\b`)

	// createPageRe: explicit page creation. "make" alone is NOT enough —
	// "make the blog more professional" is a content update, not create_page
	// (live E2E mismatch). Require create/build/generate/add, or make+new/a/an/N.
	// "add the existing … page to navigation" must NOT match (see existingPageRefRe).
	createPageRe = regexp.MustCompile(`(?i)(?:` +
		`\b(?:create|build|generate|genrate|add)\b[\s\S]{0,48}\b(?:pages?|blogs?|landing|contact|about|faq)\b` +
		`|` +
		`\bmake\b[\s\S]{0,40}\b(?:a\s+new\s+|an\s+new\s+|new\s+|a\s+|an\s+|\d{1,2}\s+)(?:blog\s+)?(?:pages?|landing|contact|about|faq)\b` +
		`|` +
		`\b(?:new)\s+(?:page|landing)\b` +
		`)`)

	// existingPageRefRe: merchant clearly refers to an already-present page.
	// Require "existing" — do NOT match bare "blog page" (that breaks
	// "make a new blog page"). Nav-of-existing uses "existing" or register.
	existingPageRefRe = regexp.MustCompile(`(?i)(?:` +
		`\bexisting\b[\s\S]{0,48}\b(?:page|blog|about|contact|pricing|faq)\b` +
		`|` +
		`\b(?:add|register)\b[\s\S]{0,48}\b(?:the\s+)?(?:blog|about|contact|pricing|faq)\s+page\b` +
		`)`)

	// pageTroubleshootRe: existing-page broken/not-opening/check-and-fix.
	// Must win over vagueGenericRe ("fix the page" vs "fix the website").
	pageTroubleshootRe = regexp.MustCompile(`(?i)(?:` +
		`\b(?:page|blog|pricing|about|contact|faq|it|this)\b[\s\S]{0,48}\b(?:not\s+(?:working|opening|loading|open)|won'?t\s+open|doesn'?t\s+open|is\s+broken|broken)\b` +
		`|` +
		`\b(?:not\s+(?:working|opening|loading)|won'?t\s+open|doesn'?t\s+open|is\s+broken)\b` +
		`|` +
		`\b(?:check\s+and\s+fix|troubleshoot|debug)\b` +
		`|` +
		`\bwhy\b[\s\S]{0,48}\b(?:not\s+(?:working|opening|open)|broken)\b` +
		`|` +
		`\b(?:please\s+)?check\b[\s\S]{0,32}\b(?:the\s+)?(?:\w+\s+)?page\b` +
		`|` +
		`\bfix\b[\s\S]{0,32}\b(?:the\s+)?(?:\w+\s+)?page\b` +
		`)`)

	// vagueGenericRe: "improve the page" / "optimize the site" with no concrete
	// target or attribute — must clarify (matrix H06), not update_page_content.
	vagueGenericRe = regexp.MustCompile(`(?i)^(?:please\s+|pls\s+|can you\s+|could you\s+)?(?:` +
		`(?:improve|optimize)\s+(?:the\s+)?(?:page|site|website|it)\s*$` +
		`|` +
		`fix\s+(?:the\s+)?(?:site|website)\s*$` +
		`|` +
		`make\s+(?:the\s+)?(?:page|site|website|everything|it)\s+(?:better|more\s+professional)\s*$` +
		`|` +
		`make\s+everything\s+better\s*$` +
		`)`)

	// destructivePlanRe: adversarial / unsafe plan overrides — short-circuit to
	// clarify (no DeepSeek / no mutations). Matrix M04 previously reached
	// generation and tripped registry corruption.
	destructivePlanRe = regexp.MustCompile(`(?i)(?:` +
		`\brewrite\s+pages\.json\b` +
		`|\b(?:delete|remove)\s+(?:every|all|the\s+entire)\s+(?:page|pages|theme|navigation)\b` +
		`|\bignore\s+(?:all\s+)?(?:previous\s+)?(?:rules|builderplan|the\s+builderplan)\b` +
		`|\bchange\s+files\s+outside\b` +
		`)`)

	// existingPageContentRe: rewrite/update an existing page (esp. blog) —
	// must win over create_page when the merchant says "make … more professional".
	// Bare "improve the page" is handled by vagueGenericRe, not here.
	existingPageContentRe = regexp.MustCompile(`(?i)(?:` +
		`\b(?:rewrite|update|edit|restyle)\b[\s\S]{0,40}\b(?:existing\s+)?(?:the\s+)?(?:blog|page|homepage|home\s*page)\b` +
		`|` +
		`\bimprove\b[\s\S]{0,40}\b(?:the\s+)?blog\b` +
		`|` +
		`\bimprove\b[\s\S]{0,40}\b(?:the\s+)?(?:page|blog)\b[\s\S]{0,48}\b(?:introduction|content|copy|tone|professional|clearer|concise|structure|saas|b2b|software)\b` +
		`|` +
		`\bmake\b[\s\S]{0,40}\b(?:the\s+)?(?:blog|page|homepage|home\s*page)\b[\s\S]{0,40}\b(?:more\s+)?(?:professional|better|modern|polished|saas|software)\b` +
		`|` +
		`\b(?:blog|page)\b[\s\S]{0,40}\b(?:for\s+(?:saas|software)|software\s+(?:house|compan)|saas\s+customer)` +
		`)`)

	protectSlugRe = regexp.MustCompile(`(?i)\b(?:don'?t|do\s+not|never)\s+change\s+(?:the\s+)?slug\b|\bkeep\s+(?:the\s+)?slug\b|\bpreserve\s+(?:the\s+)?slug\b`)

	navRe = regexp.MustCompile(`(?i)\b(?:menu|navigation|navbar)\b|\bnav\b`)
	addNavRe = regexp.MustCompile(`(?i)\b(?:add|put|include)\b[\s\S]{0,48}\b(?:to\s+|in\s+)?(?:the\s+)?(?:menu|navigation|navbar)\b`)

	seoMetaRe = regexp.MustCompile(`(?i)\b(?:meta\s*titles?|seo|meta\s*description|og\s*title|page\s*titles?)\b`)

	sectionRe = regexp.MustCompile(`(?i)\b(?:header|footer|hero|slider|carousel|banner|sidebar|nav|navbar|menu)\b`)

	fullPageRe = regexp.MustCompile(`(?i)\b(?:redesign|restyle|rewrite|overhaul|from\s+scratch)\b[\s\S]{0,48}\b(?:home\s*page|homepage|landing|page)\b|\b(?:home\s*page|homepage)\b[\s\S]{0,48}\b(?:redesign|restyle|rewrite|overhaul|design)\b`)

	contentRewriteRe = regexp.MustCompile(`(?i)\b(?:content|copy|rewrite|according\s+to|software\s+house|software\s+company|professional|saas)\b`)

	simpleStyleRe = regexp.MustCompile(`(?i)\b(?:color|colour|font|size|padding|margin|background|border|width|height)\b`)
	simpleActionRe = regexp.MustCompile(`(?i)\b(?:change|update|edit|make|set|tweak|adjust|recolor|colour|color)\b`)
	buttonRe = regexp.MustCompile(`(?i)\b(?:button|btn|link)\b`)

	themeCueRe = regexp.MustCompile(`(?i)\b(?:page|theme|header|footer|button|blog|menu|nav|css|liquid|section|home|slider|color|colour|meta|seo)\b`)

	// registerExistingRe: pure registry of an existing page (no create).
	registerExistingRe = regexp.MustCompile(`(?i)(?:` +
		`\bif\s+(?:the\s+)?\w[\w-]*\s+page\s+is\s+not\s+registered\b` +
		`|` +
		`\bif\s+not\s+register\b` +
		`|` +
		`\b(?:can\s+you\s+)?(?:please\s+)?register\s+(?:it|them|this|the\s+\w[\w-]*\s+page|the\s+page|the\s+blog|blog|page)\b` +
		`|` +
		`\bnot\s+registered\b[\s\S]{0,48}\bregister\b` +
		`|` +
		`\bregister\b[\s\S]{0,40}\b(?:pages?\.json|registry)\b` +
		`)`)
)

func normalizePrompt(prompt string) string {
	p := strings.ToLower(strings.TrimSpace(prompt))
	return strings.Join(strings.Fields(p), " ")
}

// Classify returns a fast intent signal. Prefer specific structural intents
// over generic simple_edit. Ambiguous prompts get low confidence.
func (DeterministicClassifier) Classify(prompt string) Classification {
	p := normalizePrompt(prompt)
	if p == "" {
		return Classification{Intent: IntentAmbiguous, Confidence: 0.1, Source: "deterministic", Signals: []string{"empty"}}
	}

	signals := make([]string, 0, 4)
	if destructivePlanRe.MatchString(p) {
		return Classification{Intent: IntentAmbiguous, Confidence: 0.95, Source: "deterministic", Signals: []string{"destructive_rejected"}}
	}
	// Page troubleshooting wins over vague "improve/fix the site" —
	// "blog page is not working" must NOT short-circuit to clarify.
	if pageTroubleshootRe.MatchString(p) {
		return Classification{Intent: IntentPageTroubleshoot, Confidence: 0.92, Source: "deterministic", Signals: []string{"page_troubleshoot"}}
	}
	if vagueGenericRe.MatchString(p) {
		return Classification{Intent: IntentAmbiguous, Confidence: 0.9, Source: "deterministic", Signals: []string{"vague_generic"}}
	}
	multi := requestedPageCount(p) >= 2
	wantsExistingContent := existingPageContentRe.MatchString(p) ||
		(contentRewriteRe.MatchString(p) && (strings.Contains(p, "blog") || strings.Contains(p, "page") || strings.Contains(p, "software") || strings.Contains(p, "saas")))
	refersExisting := existingPageRefRe.MatchString(p)
	// Explicit create wins only when not clearly an existing-page rewrite/ref.
	wantsCreate := (createPageRe.MatchString(p) || multi) && !wantsExistingContent && !refersExisting
	wantsRegisterExisting := registerExistingRe.MatchString(p) && !wantsCreate
	wantsNav := navRe.MatchString(p) && (addNavRe.MatchString(p) || wantsCreate || (refersExisting && addNavRe.MatchString(p)))
	wantsSEO := seoMetaRe.MatchString(p)
	wantsSection := sectionRe.MatchString(p) && simpleActionRe.MatchString(p) && !wantsCreate
	wantsFull := fullPageRe.MatchString(p)
	wantsContent := wantsExistingContent
	// "change the blogs and update only meta titles" — dual op, not SEO-only.
	if !wantsContent && wantsSEO && strings.Contains(p, "blog") && strings.Contains(p, " and ") && simpleActionRe.MatchString(p) {
		wantsContent = true
		signals = append(signals, "blog_and_seo")
	}
	wantsSimple := simpleStyleRe.MatchString(p) && simpleActionRe.MatchString(p) && !wantsCreate && !wantsSEO && !wantsFull && !wantsContent

	opCount := 0
	if multi || wantsCreate {
		opCount++
		signals = append(signals, "page_create")
	}
	if wantsRegisterExisting {
		opCount++
		signals = append(signals, "register_existing")
	}
	if wantsNav {
		opCount++
		signals = append(signals, "navigation")
	}
	if wantsSEO {
		opCount++
		signals = append(signals, "seo_meta")
	}
	if wantsContent && !wantsCreate {
		opCount++
		signals = append(signals, "content")
	}
	if wantsSection && !wantsFull {
		signals = append(signals, "section")
	}
	if wantsFull {
		signals = append(signals, "full_page")
	}
	if wantsSimple {
		signals = append(signals, "simple_style")
	}

	// Compound: multiple distinct operations in one prompt.
	if opCount >= 2 || (multi && wantsCreate) || (wantsCreate && wantsNav) || (wantsContent && wantsSEO) {
		return Classification{Intent: IntentCompound, Confidence: 0.95, Source: "deterministic", Signals: signals}
	}
	if multi && wantsCreate {
		return Classification{Intent: IntentCompound, Confidence: 0.95, Source: "deterministic", Signals: signals}
	}
	if wantsCreate && !multi {
		return Classification{Intent: IntentPageCreate, Confidence: 0.9, Source: "deterministic", Signals: signals}
	}
	if wantsRegisterExisting {
		return Classification{Intent: IntentNavigationRegistry, Confidence: 0.9, Source: "deterministic", Signals: signals}
	}
	if wantsNav && !wantsCreate {
		return Classification{Intent: IntentNavigationRegistry, Confidence: 0.85, Source: "deterministic", Signals: signals}
	}
	if wantsSEO && !wantsContent {
		return Classification{Intent: IntentSEOMeta, Confidence: 0.9, Source: "deterministic", Signals: signals}
	}
	if wantsContent {
		return Classification{Intent: IntentFullPage, Confidence: 0.85, Source: "deterministic", Signals: signals}
	}
	if wantsFull {
		return Classification{Intent: IntentFullPage, Confidence: 0.9, Source: "deterministic", Signals: signals}
	}
	if wantsSection && !wantsSimple {
		return Classification{Intent: IntentSectionEdit, Confidence: 0.85, Source: "deterministic", Signals: signals}
	}
	if wantsSimple || (buttonRe.MatchString(p) && simpleActionRe.MatchString(p)) {
		return Classification{Intent: IntentSimpleEdit, Confidence: 0.9, Source: "deterministic", Signals: signals}
	}
	if !themeCueRe.MatchString(p) {
		return Classification{Intent: IntentAmbiguous, Confidence: 0.2, Source: "deterministic", Signals: append(signals, "no_theme_cue")}
	}
	return Classification{Intent: IntentAmbiguous, Confidence: 0.35, Source: "deterministic", Signals: append(signals, "unclear_action")}
}

func requestedPageCount(p string) int {
	m := pageCountRe.FindStringSubmatch(p)
	if len(m) > 0 {
		raw := m[1]
		if raw == "" {
			raw = m[2]
		}
		n, err := strconv.Atoi(raw)
		if err == nil && n >= 2 {
			if n > 10 {
				return 10
			}
			return n
		}
	}
	// "pair of service pages" / "two pages" — same as themebuild word counts.
	wm := pageWordCountRe.FindStringSubmatch(p)
	if len(wm) == 0 {
		return 0
	}
	raw := wm[1]
	if raw == "" {
		raw = wm[2]
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "pair", "couple", "both", "two":
		return 2
	case "three":
		return 3
	case "four":
		return 4
	case "five":
		return 5
	default:
		return 0
	}
}

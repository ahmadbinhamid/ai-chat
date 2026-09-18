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

	createPageRe = regexp.MustCompile(`(?i)\b(?:create|make|build|generate|genrate|add)\b[\s\S]{0,48}\b(?:page|blog|landing|contact|about|faq)\b|\b(?:new)\s+(?:page|landing)`)

	navRe = regexp.MustCompile(`(?i)\b(?:menu|navigation|navbar)\b|\bnav\b`)
	addNavRe = regexp.MustCompile(`(?i)\b(?:add|put|include)\b[\s\S]{0,48}\b(?:to\s+|in\s+)?(?:the\s+)?(?:menu|navigation|navbar)\b`)

	seoMetaRe = regexp.MustCompile(`(?i)\b(?:meta\s*titles?|seo|meta\s*description|og\s*title|page\s*titles?)\b`)

	sectionRe = regexp.MustCompile(`(?i)\b(?:header|footer|hero|slider|carousel|banner|sidebar|nav|navbar|menu)\b`)

	fullPageRe = regexp.MustCompile(`(?i)\b(?:redesign|restyle|rewrite|overhaul|from\s+scratch)\b[\s\S]{0,48}\b(?:home\s*page|homepage|landing|page)\b|\b(?:home\s*page|homepage)\b[\s\S]{0,48}\b(?:redesign|restyle|rewrite|overhaul|design)\b`)

	contentRewriteRe = regexp.MustCompile(`(?i)\b(?:content|copy|rewrite|according\s+to|software\s+house|software\s+company)\b`)

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
		`\b(?:please\s+)?register\s+(?:it|them|this|the\s+page|the\s+blog|blog|page)\b` +
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
	multi := requestedPageCount(p) >= 2
	wantsCreate := createPageRe.MatchString(p) || multi
	wantsRegisterExisting := registerExistingRe.MatchString(p) && !wantsCreate
	wantsNav := navRe.MatchString(p) && (addNavRe.MatchString(p) || wantsCreate)
	wantsSEO := seoMetaRe.MatchString(p)
	wantsSection := sectionRe.MatchString(p) && simpleActionRe.MatchString(p) && !wantsCreate
	wantsFull := fullPageRe.MatchString(p)
	wantsContent := contentRewriteRe.MatchString(p) && (strings.Contains(p, "blog") || strings.Contains(p, "page") || strings.Contains(p, "software"))
	// "change the blogs and update only meta titles" — dual op, not SEO-only.
	if !wantsContent && wantsSEO && strings.Contains(p, "blog") && strings.Contains(p, " and ") && simpleActionRe.MatchString(p) {
		wantsContent = true
		signals = append(signals, "blog_and_seo")
	}
	wantsSimple := simpleStyleRe.MatchString(p) && simpleActionRe.MatchString(p) && !wantsCreate && !wantsSEO && !wantsFull

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
	if len(m) == 0 {
		return 0
	}
	raw := m[1]
	if raw == "" {
		raw = m[2]
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 2 {
		return 0
	}
	if n > 10 {
		return 10
	}
	return n
}

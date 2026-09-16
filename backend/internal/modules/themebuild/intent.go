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

	themeTargetRe = regexp.MustCompile(`(?i)\b(header|footer|nav|navbar|menu|hero|button|banner|logo|cart|sidebar|homepage|home\s*page|product\s*card|section|page|theme|layout|css|liquid|component|partial)\b`)
	themeActionRe = regexp.MustCompile(`(?i)\b(change|update|edit|redesign|restyle|improve|fix|make|tweak|adjust|modify|add|remove|create|build|rewrite|replace|move|resize|recolor|colour|color|style)\b`)
	themeQueryRe  = regexp.MustCompile(`(?i)\b(what|where|which|show|find|list|read|explain|describe|how|does|is there|look at)\b`)
	repairRe      = regexp.MustCompile(`(?i)\b(fix|broken|error|bug|crash|not working|doesn'?t work|render failed|blank|missing)\b`)
	complexRe     = regexp.MustCompile(`(?i)\b(from scratch|entire theme|whole site|all pages|every page|new theme|brand kit|redesign (the )?site|multi[- ]page)\b`)
	multiFileRe   = regexp.MustCompile(`(?i)\b(and also|as well as|both .+ and|homepage and|header and footer|all components|several files|multiple files)\b`)
)

// ClassifyIntent is a deterministic local router. Attachments / non-edit modes
// never classify as conversation. Ambiguous text defaults toward a theme path.
func ClassifyIntent(prompt, mode string, hasAttachments bool) Intent {
	if hasAttachments {
		return IntentSimpleEdit
	}
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
		return IntentConversation
	}

	// Strip trailing punctuation for exact-map lookup.
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

	if complexRe.MatchString(p) {
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
	case "header", "footer", "hero", "nav", "menu", "logo", "cart", "css", "js",
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

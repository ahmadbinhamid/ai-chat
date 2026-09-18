package themebuild

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"ai-chat/internal/ai"
)

// isAddToMenuPrompt: merchant wants a link added to the live nav
// (defaults.json menu.items) — e.g. "add Services page in menu".
func isAddToMenuPrompt(prompt string) bool {
	p := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
	if isBulkPageDeletePrompt(p) {
		return false
	}
	return addToMenuRe.MatchString(p) && menuNavRe.MatchString(p)
}

// menuLabelFromAddPrompt extracts the link label ("Services") from common
// add-to-menu phrasings. Empty when unclear — validation then only requires
// a real menu.items mutation.
var menuLabelFromAddPromptRe = regexp.MustCompile(`(?i)\b(?:add|put|include)\s+(?:a\s+|an\s+|the\s+)?([a-z][\w\s&/-]{0,40}?)\s+(?:page\s+)?(?:to|in)\s+(?:the\s+)?(?:menu|navigation|navbar)\b`)

func menuLabelFromAddPrompt(prompt string) string {
	p := strings.Join(strings.Fields(prompt), " ")
	m := menuLabelFromAddPromptRe.FindStringSubmatch(p)
	if len(m) < 2 {
		return ""
	}
	label := strings.TrimSpace(m[1])
	label = strings.TrimSuffix(strings.TrimSpace(label), " page")
	label = strings.TrimSpace(label)
	if len(label) < 2 || len(label) > 40 {
		return ""
	}
	return label
}

// incompleteAddToMenuProposal rejects no-op defaults.json edits (±0) that
// claim the nav was updated but never add the menu item — observed live:
// "Added Services to menu" / Applied / defaults.json ±0 / nav unchanged.
func incompleteAddToMenuProposal(prompt string, result *ai.Result) error {
	if result == nil || !isAddToMenuPrompt(prompt) {
		return nil
	}
	if result.NeedsClarification || result.AnsweredQuestion {
		return nil
	}
	label := menuLabelFromAddPrompt(prompt)
	var defaultsBody string
	for _, f := range result.Files {
		if strings.EqualFold(strings.TrimSpace(f.Path), "defaults.json") {
			defaultsBody = generatedFileBody(f)
			break
		}
	}
	if strings.TrimSpace(defaultsBody) == "" {
		return fmt.Errorf("add-to-menu must propose a full defaults.json update that appends the new item under menu.items — editing header.liquid alone will not change the live nav")
	}
	if !defaultsJSONHasMenuItem(defaultsBody, label) {
		if label != "" {
			return fmt.Errorf("defaults.json menu.items must include a new entry labeled %q (and a real url) — a no-op edit will not show in the storefront menu", label)
		}
		return fmt.Errorf("defaults.json menu.items must include the new nav link the merchant asked for — a no-op edit will not show in the storefront menu")
	}
	return nil
}

func defaultsJSONHasMenuItem(defaultsJSON, label string) bool {
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(defaultsJSON), &root); err != nil {
		// Fallback: substring check on label only.
		if label == "" {
			return false
		}
		return strings.Contains(strings.ToLower(defaultsJSON), strings.ToLower(label))
	}
	menuRaw, ok := root["menu"]
	if !ok {
		return false
	}
	var menu struct {
		Items []struct {
			Label string `json:"label"`
			URL   string `json:"url"`
		} `json:"items"`
	}
	if err := json.Unmarshal(menuRaw, &menu); err != nil {
		return false
	}
	if label == "" {
		return len(menu.Items) > 0
	}
	want := strings.ToLower(strings.TrimSpace(label))
	for _, it := range menu.Items {
		if strings.Contains(strings.ToLower(it.Label), want) && strings.TrimSpace(it.URL) != "" {
			return true
		}
	}
	return false
}

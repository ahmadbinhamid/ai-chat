package themebuild

import (
	"fmt"
	"strings"

	"ai-chat/internal/ai"
)

// incompleteNamedPageRewriteProposal rejects the failure mode where the
// merchant named a page (shop→products, services, …) for a content/theme
// rewrite but the model only touched unrelated components (card-essentials,
// contact-inquiry) — often with ±0 no-ops — and claimed the page was updated.
func incompleteNamedPageRewriteProposal(prompt string, result *ai.Result) error {
	if result == nil || !isPageContentRewritePrompt(prompt) {
		return nil
	}
	// Multi-page create must never be reinterpreted as a single named-page
	// rewrite — prepared context packages often mention privacy/home/etc.
	if isMultiPageCreatePrompt(prompt) || isBulkPageDeletePrompt(prompt) {
		return nil
	}
	if result.NeedsClarification || result.AnsweredQuestion {
		return nil
	}
	slug := promptNamedPageSlug(prompt)
	if slug == "" {
		return nil
	}
	want := "pages/" + slug + ".liquid"
	wantCSS := "pages/css/" + slug + ".css"
	altCSS := ""
	if slug == "products" {
		altCSS = "pages/css/product-list.css"
	}
	touchedNamed := false
	onlyJunk := true
	for _, f := range result.Files {
		low := strings.ToLower(strings.TrimSpace(f.Path))
		act := strings.ToLower(strings.TrimSpace(f.Action))
		if act != "update" && act != "edit" && act != "create" {
			continue
		}
		if low == want || low == wantCSS || (altCSS != "" && low == altCSS) {
			touchedNamed = true
			onlyJunk = false
			continue
		}
		if low == "components/card-essentials.liquid" ||
			low == "components/contact-inquiry.liquid" ||
			low == "pages/blog.liquid" {
			continue
		}
		onlyJunk = false
	}
	if touchedNamed {
		return nil
	}
	if len(result.Files) == 0 {
		return fmt.Errorf("must update %s for this page rewrite — empty proposal", want)
	}
	if onlyJunk {
		return fmt.Errorf("must update %s — editing only card-essentials/contact-inquiry/blog does not change the %s page", want, slug)
	}
	hint := want
	if altCSS != "" {
		hint = want + " and/or " + altCSS
	}
	return fmt.Errorf("must update %s (the named page) — proposal did not touch it", hint)
}

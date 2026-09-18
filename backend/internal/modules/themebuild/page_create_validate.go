package themebuild

import (
	"fmt"
	"strings"

	"ai-chat/internal/ai"
)

// incompleteMultiPageCreateProposal rejects the failure mode where the model
// claims "Generated N blog pages" but only edited blog.liquid / card-essentials
// without creating pages/*.liquid or updating pages.json.
func incompleteMultiPageCreateProposal(prompt string, result *ai.Result) error {
	if result == nil || !isMultiPageCreatePrompt(prompt) {
		return nil
	}
	if result.NeedsClarification || result.AnsweredQuestion {
		return nil
	}
	want := multiPageCreateBatchSize(prompt)
	if want < 2 {
		return nil
	}

	pageLiquids := 0
	hasPagesJSON := false
	onlyIndexOrCard := true
	for _, f := range result.Files {
		low := strings.ToLower(strings.TrimSpace(f.Path))
		act := strings.ToLower(strings.TrimSpace(f.Action))
		if low == "pages.json" && (act == "update" || act == "create") {
			hasPagesJSON = true
			onlyIndexOrCard = false
			continue
		}
		if strings.HasPrefix(low, "pages/") && strings.HasSuffix(low, ".liquid") &&
			!strings.HasPrefix(low, "pages/css/") && !strings.HasPrefix(low, "pages/auth/") {
			if act == "create" || act == "update" {
				pageLiquids++
			}
			if low != "pages/blog.liquid" && low != "pages/home.liquid" {
				onlyIndexOrCard = false
			}
			continue
		}
		if low == "components/card-essentials.liquid" || low == "pages/blog.liquid" {
			continue
		}
		onlyIndexOrCard = false
	}

	// page_registry_entry alone can only register one page — not enough for
	// N>1 in a single-shot batch (compound atomic steps use a different gate).
	if result.PageRegistryEntry != nil && !hasPagesJSON {
		return fmt.Errorf(
			"proposal/tool contract mismatch: creating %d pages in one shot requires a direct pages.json FULL-body update (page_registry_entry only registers one page) plus %d pages/<slug>.liquid creates",
			want, want)
	}

	missing := make([]string, 0, 3)
	if !hasPagesJSON {
		missing = append(missing, fmt.Sprintf("direct pages.json update that keeps every existing route and appends %d new published entries", want))
	}
	if pageLiquids < want {
		missing = append(missing, fmt.Sprintf("%d new pages/<kebab-slug>.liquid files (action create) — got %d page liquid file(s)", want, pageLiquids))
	}
	if onlyIndexOrCard && len(result.Files) > 0 {
		missing = append(missing, "do not only edit blog.liquid / card-essentials.liquid — that does not create pages")
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("incomplete multi-page create (need %d pages): %s", want, strings.Join(missing, "; "))
}

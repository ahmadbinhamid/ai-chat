package themefs

import (
	"encoding/json"
	"fmt"
)

// PageEntry mirrors one pages.json record (THEME_ENGINE_SPEC.md §5). Field
// order matches the reference theme's own entries.
type PageEntry struct {
	Title          string `json:"title"`
	Slug           string `json:"slug"`
	Path           string `json:"path"`
	Type           string `json:"type"`
	Page           string `json:"page"`
	SEOTitle       string `json:"seo_title"`
	SEODescription string `json:"seo_description"`
	SEOKeywords    string `json:"seo_keywords"`
	OGTitle        string `json:"og_title"`
	OGDescription  string `json:"og_description"`
	OGImagePath    string `json:"og_image_path"`
	Status         string `json:"status"`
	PublishedAt    string `json:"published_at"`
	RequiresAuth   bool   `json:"requires_auth"`
}

// ErrSlugAlreadyRegistered is returned when MergePageRegistration is asked to
// add a slug that already has an entry — registering a page is additive
// (a new route), never a silent overwrite of an existing one.
var ErrSlugAlreadyRegistered = fmt.Errorf("slug already registered in pages.json")

// MergePageRegistration parses the current pages.json content, appends entry
// (rejecting a duplicate slug), and returns the new file content. Pure: it
// never touches disk, so a caller can retry a failed write without
// re-deriving this step, and it's unit-testable without a filesystem fixture.
func MergePageRegistration(currentPagesJSON []byte, entry PageEntry) ([]byte, error) {
	var entries []PageEntry
	if len(currentPagesJSON) > 0 {
		if err := json.Unmarshal(currentPagesJSON, &entries); err != nil {
			return nil, fmt.Errorf("parse existing pages.json: %w", err)
		}
	}

	for _, existing := range entries {
		if existing.Slug == entry.Slug {
			return nil, ErrSlugAlreadyRegistered
		}
	}

	entries = append(entries, entry)

	out, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal updated pages.json: %w", err)
	}
	return out, nil
}

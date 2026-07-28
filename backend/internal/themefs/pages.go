package themefs

// PageEntry mirrors one pages.json record (THEME_ENGINE_SPEC.md §5) — this
// is the shape the model proposes when registering a new page. Field order
// matches the reference theme's own entries. Unlike before, this service no
// longer merges these into pages.json itself: flowpos-backend's own
// theme-file API does that server-side (see Store.WriteFile's PageMeta) —
// this type only exists now to give the model's structured output a place
// to land, and to translate into a PageMeta when writing the file.
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

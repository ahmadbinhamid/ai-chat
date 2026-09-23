package themefs

// PageEntry mirrors one pages.json record (§5) — the shape the model proposes when registering a new page.
// flowpos-backend's theme-file API merges it into pages.json server-side; this type just holds the model's output and translates to PageMeta.
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

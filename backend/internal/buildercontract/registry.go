package buildercontract

// PageIdentityRule freezes how a custom page is identified.
type PageIdentityRule struct {
	// PageKey is pages.json "page" — equals liquid basename (no extension).
	PageKey string
	// Slug is pages.json "slug" — for type=custom must equal PageKey.
	Slug string
	// RelativePath is theme-relative liquid path.
	RelativePath string // pages/<slug>.liquid or pages/auth/<slug>.liquid
	// RegistryPath is pages.json "path" directory hint: /pages or /pages/auth.
	RegistryPath string
}

// CustomPagePath returns the canonical liquid path for a non-auth custom page.
func CustomPagePath(slug string) string {
	return "pages/" + slug + ".liquid"
}

// AuthPagePath returns the canonical liquid path under pages/auth/.
func AuthPagePath(slug string) string {
	return "pages/auth/" + slug + ".liquid"
}

// RegistryField names allowed on a pages.json row (theme_engine_spec §5 / PageEntry).
// Do not invent additional fields in contracts or ML outputs.
const (
	RegFieldTitle          = "title"
	RegFieldSlug           = "slug"
	RegFieldPath           = "path"
	RegFieldType           = "type"
	RegFieldPage           = "page"
	RegFieldSEOTitle       = "seo_title"
	RegFieldSEODescription = "seo_description"
	RegFieldSEOKeywords    = "seo_keywords"
	RegFieldOGTitle        = "og_title"
	RegFieldOGDescription  = "og_description"
	RegFieldOGImagePath    = "og_image_path"
	RegFieldStatus         = "status"
	RegFieldPublishedAt    = "published_at"
	RegFieldRequiresAuth   = "requires_auth"
)

// RegistryFields is the closed set of pages.json entry fields.
func RegistryFields() []string {
	return []string{
		RegFieldTitle, RegFieldSlug, RegFieldPath, RegFieldType, RegFieldPage,
		RegFieldSEOTitle, RegFieldSEODescription, RegFieldSEOKeywords,
		RegFieldOGTitle, RegFieldOGDescription, RegFieldOGImagePath,
		RegFieldStatus, RegFieldPublishedAt, RegFieldRequiresAuth,
	}
}

// SEOFields are the only fields update_seo_meta may change unless slug change is explicitly supported later.
func SEOFields() []string {
	return []string{
		RegFieldSEOTitle, RegFieldSEODescription, RegFieldSEOKeywords,
		RegFieldOGTitle, RegFieldOGDescription, RegFieldOGImagePath,
	}
}

// PublishStatus values for pages.json status.
const (
	StatusPublished = "published"
	StatusDraft     = "draft"
)

// RegistryRule captures pages.json invariants (contract_version=1).
type RegistryRule struct {
	ID          string
	Description string
	Enforced    bool   // true when current AI/FlowPOS path enforces it
	Authority   string // which layer owns the check
}

// RegistryRules returns frozen registry invariants.
func RegistryRules() []RegistryRule {
	return []RegistryRule{
		{
			ID: "page_file_equals_basename",
			Description: "pages.json page field must equal the liquid basename " +
				"(pages/<page>.liquid or pages/auth/<page>.liquid).",
			Enforced: true, Authority: "themecheck page-route + FlowPOS PageJsonService",
		},
		{
			ID: "custom_slug_equals_page",
			Description: "For type=custom, slug must equal page (basename).",
			Enforced: true, Authority: "theme_engine_spec §5 + themecheck",
		},
		{
			ID: "create_requires_registry",
			Description: "Creating pages/<slug>.liquid without a matching registry entry is incomplete and must not stage.",
			Enforced: true, Authority: "themecheck page-route + page_registry_consistency",
		},
		{
			ID: "multi_create_one_entry_forbidden",
			Description: "N new page files require N registry entries (or N page_registry_entry steps via compound). One entry for many files is rejected.",
			Enforced: true, Authority: "incompleteMultiPageCreateProposal / ensureProposedCreatesRegistered",
		},
		{
			ID: "registry_row_file_exists",
			Description: "Every pages.json row must resolve to an existing liquid file (theme integrity).",
			Enforced: true, Authority: "FlowPOS PageJsonService::loadAndAssertIntact",
		},
		{
			ID: "unique_slugs",
			Description: "slug/page keys must be unique within a theme.",
			Enforced: true, Authority: "FlowPOS PageJsonService",
		},
		{
			ID: "omit_status_is_draft",
			Description: "Omitted status defaults to draft. Draft pages 404 on live storefront (prod); visible in dashboard preview (dev).",
			Enforced: true, Authority: "theme_engine_spec §5 + storefront",
		},
		{
			ID: "published_preferred_on_create",
			Description: "When merchant expects a live/shopper-visible page, status must be published. AI create path does not always hard-gate this today.",
			Enforced: false, Authority: "CONTRACT GAP — soft guidance in theme_engine_spec; not always hard AI gate",
		},
		{
			ID: "no_full_pages_json_rewrite_for_single_upsert",
			Description: "Prefer page_registry_entry / structured merge over model full rewrite of pages.json for single-page upserts.",
			Enforced: true, Authority: "theme_engine_spec §5 + pages_registry_merge",
		},
	}
}

package buildercontract

// RenderVariable documents one storefront Liquid context variable
// (theme_engine_spec §7). Models must not invent fields outside this set.
type RenderVariable struct {
	Name        string
	Meaning     string
	Source      string
	Required    bool   // typically present on every page render
	PageTypes   string // which page types commonly use it
	KeyFields   string // important fields (summary)
}

// RenderVariables returns the verified render context contract.
func RenderVariables() []RenderVariable {
	return []RenderVariable{
		{Name: "environment", Meaning: "prod (live) or dev (dashboard preview)", Source: "storefront engine", Required: true, PageTypes: "all", KeyFields: "prod|dev"},
		{Name: "request", Meaning: "current HTTP request", Source: "storefront engine", Required: true, PageTypes: "all", KeyFields: "path, query"},
		{Name: "csrf_token", Meaning: "CSRF for storefront API writes; empty in dashboard preview", Source: "storefront engine", Required: true, PageTypes: "all", KeyFields: "string"},
		{Name: "store", Meaning: "current store identity", Source: "tenant/store", Required: true, PageTypes: "all", KeyFields: "id, name, tenant_id, is_guest_checkout"},
		{Name: "theme", Meaning: "active theme metadata + asset base", Source: "theme package", Required: true, PageTypes: "all", KeyFields: "id, store_id, slug, asset_base"},
		{Name: "page", Meaning: "current pages.json row projected into Liquid", Source: "pages.json", Required: true, PageTypes: "all registered routes", KeyFields: "title, slug, page, seo_*, og_*"},
		{Name: "products", Meaning: "product list + pagination", Source: "catalogue", Required: false, PageTypes: "home, products, category lists", KeyFields: "items[], pagination"},
		{Name: "product", Meaning: "single product detail", Source: "catalogue", Required: false, PageTypes: "product detail", KeyFields: "name/title, variants, images, addons, …"},
		{Name: "categories", Meaning: "category list + pagination", Source: "catalogue", Required: false, PageTypes: "categories", KeyFields: "items[], pagination"},
		{Name: "category", Meaning: "single category", Source: "catalogue", Required: false, PageTypes: "category detail", KeyFields: "name, slug, description, url, image_url"},
		{Name: "filters", Meaning: "listing filter state echoed from query", Source: "request query", Required: false, PageTypes: "products/category listings", KeyFields: "search, sort, category, min_price, max_price, per_page"},
		{Name: "filter_categories", Meaning: "filter pills (up to 50)", Source: "catalogue", Required: false, PageTypes: "listings", KeyFields: "[{slug, name}]"},
		{Name: "filter_price_range", Meaning: "catalogue price bounds", Source: "catalogue", Required: false, PageTypes: "listings", KeyFields: "min, max"},
		{Name: "basket", Meaning: "shopper basket; nil until created — guard with {% if basket %}", Source: "basket service", Required: false, PageTypes: "all (header/minicart)", KeyFields: "items[], totals, customer_*"},
		{Name: "customer", Meaning: "logged-in customer; nil when logged out", Source: "auth", Required: false, PageTypes: "all / account", KeyFields: "id, name, email, phone"},
		{Name: "auth_check", Meaning: "customer authenticated (bool-ish); passed as customer_authenticated to layout", Source: "auth", Required: true, PageTypes: "all", KeyFields: "true/false/1/0"},
		{Name: "settings", Meaning: "decoded defaults.json", Source: "defaults.json", Required: true, PageTypes: "all", KeyFields: "colors, font, layout, header, menu, footer, …"},
		{Name: "menu", Meaning: "navigation object from defaults.json menu", Source: "defaults.json → menu", Required: true, PageTypes: "all (header)", KeyFields: "items[] → id, label, url, children[]"},
		{Name: "path", Meaning: "legacy alias of current path (prefer request.path)", Source: "request", Required: true, PageTypes: "layout/header", KeyFields: "string"},
	}
}

// LiquidCapability documents supported Liquid surface (theme_engine_spec §1).
type LiquidCapability struct {
	Kind        string // tag | filter | custom_tag | rule
	Name        string
	Description string
}

// LiquidCapabilities returns the generation vocabulary (not the entire Keepsuit stdlib).
func LiquidCapabilities() []LiquidCapability {
	return []LiquidCapability{
		{Kind: "tag", Name: "render", Description: "Only include mechanism; path must be liquid/... or components/...; isolated scope"},
		{Kind: "tag", Name: "if/elsif/else/endif", Description: "Conditionals"},
		{Kind: "tag", Name: "for/endfor", Description: "Loops; forloop.first/last"},
		{Kind: "tag", Name: "assign", Description: "Variable assignment"},
		{Kind: "tag", Name: "capture/endcapture", Description: "String capture"},
		{Kind: "tag", Name: "comment/endcomment", Description: "Comments"},
		{Kind: "custom_tag", Name: "content_for_header|body|footer", Description: "Layout-only tags in liquid/layout-*.liquid"},
		{Kind: "filter", Name: "default, append, asset_url, plus, size, slice, strip, upcase, money, get_products, escape, strip_html, truncate", Description: "Preferred filters"},
		{Kind: "rule", Name: "no_schema_section_include", Description: "No {% schema %}, {% section %}, or {% include %}"},
		{Kind: "rule", Name: "nil_not_blank", Description: "In this engine nil == blank is FALSE; coerce with | default before append"},
		{Kind: "rule", Name: "page_boilerplate", Description: "Every pages/*.liquid must open/close with layout-start/layout-end render (spec §3)"},
		{Kind: "rule", Name: "assets_via_asset_url", Description: "Static assets via {{ 'images/…' | asset_url }}; no hardcoded theme-assets paths"},
	}
}

// ThemeRootEntries are required/expected theme root paths.
func ThemeRootEntries() []string {
	return []string{
		"pages.json",
		"defaults.json",
		"pages/",
		"pages/auth/",
		"liquid/",
		"components/",
		"css/",
		"js/",
		"images/",
	}
}

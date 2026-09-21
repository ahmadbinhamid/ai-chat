package buildercontract

// NavigationRule freezes defaults.json menu contract.
type NavigationRule struct {
	ID          string
	Description string
	Enforced    bool
	Authority   string
}

// Menu item field names (theme_engine_spec §6 / menuItemSchema).
const (
	MenuFieldID       = "id"
	MenuFieldLabel    = "label"
	MenuFieldURL      = "url"
	MenuFieldChildren = "children"
	MenuFieldPageID   = "pageId" // optional
)

// CanonicalMenuIdentity is the only menu object AI Builder merges today.
const CanonicalMenuIdentity = "menu"

// MenuItemFields is the closed set for menu.items[] entries.
func MenuItemFields() []string {
	return []string{MenuFieldID, MenuFieldLabel, MenuFieldURL, MenuFieldChildren, MenuFieldPageID}
}

// NavigationRules returns frozen menu invariants.
func NavigationRules() []NavigationRule {
	return []NavigationRule{
		{
			ID: "nav_file_is_defaults_json",
			Description: "Navigation lives in theme-root defaults.json → menu.items[].",
			Enforced: true, Authority: "theme_engine_spec §6 + menu_merge",
		},
		{
			ID: "structured_merge_only",
			Description: "add_to_navigation must structured-merge; never ask the model for a full defaults.json rewrite.",
			Enforced: true, Authority: "themebuild menu_merge / compound menu step",
		},
		{
			ID: "append_default_position",
			Description: "Default insert position is append.",
			Enforced: true, Authority: "AddToMenuOperation",
		},
		{
			ID: "duplicate_menu_item_rejected",
			Description: "Duplicate menu items (same id/url/page) are rejected or no-op.",
			Enforced: true, Authority: "menu_merge menuItemAlreadyPresent",
		},
		{
			ID: "nav_independent_of_registry",
			Description: "Registration and navigation are separate operations. Adding to nav does not register a page; registering does not add nav.",
			Enforced: true, Authority: "builderoperations RegisterExistingPage + menu_merge",
		},
		{
			ID: "menu_item_should_resolve_to_registered_page",
			Description: "When add_to_navigation targets a theme page, identity SHOULD resolve to a registered page. FlowPOS/AI path does not fully hard-enforce menu→registry today; unregistered menu URLs are possible.",
			Enforced: false, Authority: "CONTRACT SOFT — document only; optional future hard check",
		},
		{
			ID: "create_does_not_auto_nav",
			Description: "create_page does not automatically add navigation unless explicitly requested (add_to_navigation / compound menu step).",
			Enforced: true, Authority: "compound_workflow + planner",
		},
	}
}

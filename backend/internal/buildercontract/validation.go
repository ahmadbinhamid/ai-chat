package buildercontract

// ProtectedField is a field/identity ML and DeepSeek must not casually rewrite.
type ProtectedField struct {
	Name        string
	Scope       string // page | registry | navigation | theme
	Description string
}

// ProtectedFields returns identity and structural fields that require explicit intent.
func ProtectedFields() []ProtectedField {
	return []ProtectedField{
		{Name: "page", Scope: "registry", Description: "pages.json page key / liquid basename"},
		{Name: "slug", Scope: "registry", Description: "URL slug; for custom pages equals page"},
		{Name: "path", Scope: "registry", Description: "/pages or /pages/auth"},
		{Name: "type", Scope: "registry", Description: "System types are fixed; home must stay type=home"},
		{Name: "status", Scope: "registry", Description: "published vs draft; never silently unpublish"},
		{Name: "liquid_path", Scope: "page", Description: "pages/<slug>.liquid path identity"},
		{Name: "menu.items", Scope: "navigation", Description: "Unrelated menu entries must be preserved"},
		{Name: "pages.json_other_routes", Scope: "registry", Description: "Unrelated registry rows must be preserved"},
		{Name: "defaults.json_non_menu", Scope: "theme", Description: "colors/fonts/layout/header/footer unless explicitly requested"},
	}
}

// ValidationRule is a staging/apply gate.
type ValidationRule struct {
	ID          string
	Description string
	When        string
	Enforced    bool
}

// ValidationRules returns contract validation gates.
func ValidationRules() []ValidationRule {
	return []ValidationRule{
		{ID: "page_route", Description: "New page liquid create must match page_registry_entry / pages.json", When: "before stage", Enforced: true},
		{ID: "page_file_registry_consistency", Description: "validatePageFileRegistryConsistency on creates", When: "before stage", Enforced: true},
		{ID: "boilerplate", Description: "layout-start/end on pages/*.liquid", When: "themecheck", Enforced: true},
		{ID: "pages_json_valid", Description: "Merged pages.json must remain valid JSON with unique slugs", When: "merge/apply", Enforced: true},
		{ID: "defaults_json_valid", Description: "Menu merge must leave valid defaults.json", When: "menu merge", Enforced: true},
		{ID: "plan_execution_match", Description: "BuilderPlan operation class must match execution class; mismatch must not silently continue when correctness is compromised", When: "observe/execute", Enforced: false}, // partial today
		{ID: "compound_n_ge_2", Description: "N>=2 page creations (including word counts pair/two/couple/…) must enter compound workflow", When: "intent routing", Enforced: true},
	}
}

// ActiveTarget is the chat-scoped follow-up resolution contract.
// Persistence across process restart is NOT part of contract_version=1
// (in-memory only today — see themebuild activeTargetCache).
type ActiveTarget struct {
	Type            string `json:"type"` // "page"
	Page            string `json:"page"`
	Slug            string `json:"slug"`
	Path            string `json:"path"`
	LastOperation   string `json:"last_operation"`
	ContractVersion string `json:"contract_version"`
}

// ActiveTargetContract documents scoping and bounds.
type ActiveTargetContract struct {
	TenantScoped       bool
	ChatScoped         bool
	Bounded            bool
	MaxEntries         int
	Persisted          bool // false in v1
	SafeToEvict        bool
	ContractVersionKey string
}

// DefaultActiveTargetContract returns the v1 active-target rules.
func DefaultActiveTargetContract() ActiveTargetContract {
	return ActiveTargetContract{
		TenantScoped:       true,
		ChatScoped:         true,
		Bounded:            true,
		MaxEntries:         512,
		Persisted:          false, // separate future task
		SafeToEvict:        true,
		ContractVersionKey: ContractVersion,
	}
}

// TargetResolutionOrder is how troubleshoot/follow-up resolves "this page".
func TargetResolutionOrder() []string {
	return []string{
		"explicit_page_name_in_prompt",
		"builder_plan_operation_target",
		"builder_plan_targets",
		"active_target_for_tenant_chat",
		"clarification_if_unresolvable",
	}
}

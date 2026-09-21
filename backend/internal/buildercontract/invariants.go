package buildercontract

// PlanExecutionInvariant: BuilderPlan operation MUST equal actual execution class.
type PlanExecutionInvariant struct {
	ID          string
	Description string
	OnMismatch  string
}

// PlanExecutionInvariants returns hard plan↔execution rules for contract_version=1.
func PlanExecutionInvariants() []PlanExecutionInvariant {
	return []PlanExecutionInvariant{
		{
			ID:          "op_kind_equals_execution",
			Description: "Planned PageOperation must match the execution class that runs (create≠update, register≠nav, diagnose≠create).",
			OnMismatch:  "record diagnostic; fail safely when correctness would be compromised; do not silently continue",
		},
		{
			ID:          "create_not_existing_update",
			Description: "create_page must not become update of an existing page without explicit recreate semantics.",
			OnMismatch:  "fail / reclassify; never silently rewrite existing identity as create",
		},
		{
			ID:          "update_not_create",
			Description: "update_page_content / regenerate must not invent a new slug or second registry row.",
			OnMismatch:  "reject proposal / repair toward preserve-identity",
		},
		{
			ID:          "register_existing_no_content",
			Description: "register_existing_page must not generate page content or modify navigation.",
			OnMismatch:  "strip / fail; DeepSeek=0 path",
		},
		{
			ID:          "add_to_nav_defaults_only",
			Description: "add_to_navigation mutates defaults.json menu only via structured merge.",
			OnMismatch:  "reject full defaults.json model rewrite",
		},
		{
			ID:          "diagnose_first_for_troubleshoot",
			Description: "page_troubleshoot prompts map to diagnose_existing_page, not generic ambiguous clarify, when a target can be resolved.",
			OnMismatch:  "route to diagnose; clarify only if target unresolvable",
		},
		{
			ID:          "not_implemented_not_silent_map",
			Description: "CONTRACT_DEFINED_NOT_IMPLEMENTED ops must not be silently mapped to another op without recording the gap.",
			OnMismatch:  "do not fake implementation",
		},
	}
}

// CompoundContract freezes multi-page create rules.
type CompoundContract struct {
	MinPages            int
	WordQuantities      map[string]int
	RequirePerPageEntry bool
	ForbidSingleEntry   bool
}

// DefaultCompoundContract returns N>=2 compound rules.
func DefaultCompoundContract() CompoundContract {
	return CompoundContract{
		MinPages: 2,
		WordQuantities: map[string]int{
			"pair": 2, "couple": 2, "both": 2, "two": 2,
			"three": 3, "four": 4, "five": 5,
			"six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10,
		},
		RequirePerPageEntry: true,
		ForbidSingleEntry:   true,
	}
}

// DiagnosisOutput fields required from diagnose_existing_page.
func DiagnosisOutputFields() []string {
	return []string{
		"PageExists",
		"Registered",
		"IdentityValid", // contract name; impl may use RouteValid + Registered
		"RouteValid",
		"Status", // contract; current Diagnosis may omit — gap noted in MD
		"TemplateValid",
		"NavigationState", // contract; impl uses NavigationRegistered
		"Issues",
	}
}

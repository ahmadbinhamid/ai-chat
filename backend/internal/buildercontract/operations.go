package buildercontract

// PageOperation is a canonical page-lifecycle operation kind.
// Strings match BuilderPlan OperationKind values where implemented.
type PageOperation string

const (
	OpCreatePage              PageOperation = "create_page"
	OpUpdatePageContent       PageOperation = "update_page_content"
	OpUpdateSEOMeta           PageOperation = "update_seo_meta"
	OpSimpleStyleEdit         PageOperation = "simple_style_edit"
	OpSectionEdit             PageOperation = "section_edit"
	OpFullPageEdit            PageOperation = "full_page_edit"
	OpRegeneratePageContent   PageOperation = "regenerate_page_content"
	OpRecreatePage            PageOperation = "recreate_page"
	OpRegisterPage            PageOperation = "register_page" // companion of create_page (new identity)
	OpRegisterExistingPage    PageOperation = "register_existing_page"
	OpUnregisterPage          PageOperation = "unregister_page"
	OpAddToNavigation         PageOperation = "add_to_navigation"
	OpRemoveFromNavigation    PageOperation = "remove_from_navigation"
	OpDuplicatePage           PageOperation = "duplicate_page"
	OpDiagnoseExistingPage    PageOperation = "diagnose_existing_page"
	OpFixExistingPage         PageOperation = "fix_existing_page"
	OpClarify                 PageOperation = "clarify"
)

// ImplementationStatus describes whether runtime code executes the op.
type ImplementationStatus string

const (
	// StatusImplemented — first-class plan + execution path exists.
	StatusImplemented ImplementationStatus = "implemented"
	// StatusContractDefinedNotImplemented — vocabulary frozen; no safe executor yet.
	StatusContractDefinedNotImplemented ImplementationStatus = "CONTRACT_DEFINED_NOT_IMPLEMENTED"
	// StatusPartial — planned and partially executed (e.g. diagnose may autofix registry).
	StatusPartial ImplementationStatus = "partial"
)

// ExecutionClass describes how the operation is meant to run.
type ExecutionClass string

const (
	ClassDeterministic ExecutionClass = "deterministic"
	ClassModelAssisted ExecutionClass = "model_assisted"
	ClassModelRequired ExecutionClass = "model_required"
)

// MutationFlags describe which stores an operation may touch.
type MutationFlags struct {
	PageFile     bool // pages/<slug>.liquid (create/update/delete)
	PagesJSON    bool // pages.json registry
	DefaultsJSON bool // defaults.json (typically menu.items)
	NewIdentity  bool // creates a new page/slug identity
	PreserveID   bool // must keep existing page/slug identity
}

// LifecycleRule is the machine-readable contract for one PageOperation.
type LifecycleRule struct {
	Operation      PageOperation
	Status         ImplementationStatus
	ExecutionClass ExecutionClass
	ExistingPage   bool // target must already exist
	NewIdentity    bool
	Mutations      MutationFlags
	DeepSeek       DeepSeekPolicy
	BuilderPlanOp  string // matching builderplan.OperationKind string; empty if not yet wired
	Summary        string
}

// DeepSeekPolicy constrains model usage for an operation.
type DeepSeekPolicy string

const (
	DeepSeekForbidden DeepSeekPolicy = "forbidden" // local/deterministic only
	DeepSeekOptional  DeepSeekPolicy = "optional"  // may assist after deterministic work
	DeepSeekRequired  DeepSeekPolicy = "required"  // content generation needed
	DeepSeekNA        DeepSeekPolicy = "n/a"       // clarify / no generation
)

// AllLifecycleRules returns the frozen contract matrix (contract_version=1).
func AllLifecycleRules() []LifecycleRule {
	return []LifecycleRule{
		{
			Operation: OpCreatePage, Status: StatusImplemented, ExecutionClass: ClassModelRequired,
			ExistingPage: false, NewIdentity: true,
			Mutations:     MutationFlags{PageFile: true, PagesJSON: true, NewIdentity: true},
			DeepSeek:      DeepSeekRequired,
			BuilderPlanOp: "create_page",
			Summary:       "Create brand-new page identity: pages/<slug>.liquid + pages.json entry. Nav not automatic.",
		},
		{
			Operation: OpRegisterPage, Status: StatusImplemented, ExecutionClass: ClassModelAssisted,
			ExistingPage: false, NewIdentity: true,
			Mutations:     MutationFlags{PagesJSON: true, NewIdentity: true},
			DeepSeek:      DeepSeekOptional,
			BuilderPlanOp: "register_page",
			Summary:       "Companion registration for a newly created page (page_registry_entry / pages.json merge).",
		},
		{
			Operation: OpUpdatePageContent, Status: StatusImplemented, ExecutionClass: ClassModelRequired,
			ExistingPage: true, NewIdentity: false,
			Mutations:     MutationFlags{PageFile: true, PreserveID: true},
			DeepSeek:      DeepSeekRequired,
			BuilderPlanOp: "update_page_content",
			Summary:       "Modify existing page content/style while preserving page identity (slug/page/path).",
		},
		{
			Operation: OpFullPageEdit, Status: StatusImplemented, ExecutionClass: ClassModelRequired,
			ExistingPage: true, NewIdentity: false,
			Mutations:     MutationFlags{PageFile: true, PreserveID: true},
			DeepSeek:      DeepSeekRequired,
			BuilderPlanOp: "full_page_edit",
			Summary:       "Broad rewrite of an existing page file; identity preserved.",
		},
		{
			Operation: OpSectionEdit, Status: StatusImplemented, ExecutionClass: ClassModelRequired,
			ExistingPage: true, NewIdentity: false,
			Mutations:     MutationFlags{PageFile: true, PreserveID: true},
			DeepSeek:      DeepSeekRequired,
			BuilderPlanOp: "section_edit",
			Summary:       "Edit a named section of an existing page; identity preserved.",
		},
		{
			Operation: OpSimpleStyleEdit, Status: StatusImplemented, ExecutionClass: ClassModelAssisted,
			ExistingPage: true, NewIdentity: false,
			Mutations:     MutationFlags{PageFile: true, PreserveID: true},
			DeepSeek:      DeepSeekRequired,
			BuilderPlanOp: "simple_style_edit",
			Summary:       "Narrow style/CSS-oriented edit on an existing target; identity preserved.",
		},
		{
			Operation: OpUpdateSEOMeta, Status: StatusImplemented, ExecutionClass: ClassModelAssisted,
			ExistingPage: true, NewIdentity: false,
			Mutations:     MutationFlags{PagesJSON: true, PreserveID: true},
			DeepSeek:      DeepSeekOptional,
			BuilderPlanOp: "update_seo_meta",
			Summary:       "Update SEO/OG fields on an existing registry row; preserve unrequested fields and identity.",
		},
		{
			Operation: OpRegeneratePageContent, Status: StatusContractDefinedNotImplemented, ExecutionClass: ClassModelRequired,
			ExistingPage: true, NewIdentity: false,
			Mutations: MutationFlags{PageFile: true, PreserveID: true},
			DeepSeek:  DeepSeekRequired,
			Summary:   "Rebuild content of EXISTING page; same slug/page/route/registry identity. Not create/recreate.",
		},
		{
			Operation: OpRecreatePage, Status: StatusContractDefinedNotImplemented, ExecutionClass: ClassModelRequired,
			ExistingPage: true, NewIdentity: true,
			Mutations: MutationFlags{PageFile: true, PagesJSON: true, NewIdentity: true},
			DeepSeek:  DeepSeekRequired,
			Summary:   "Replace existing identity with a NEW identity. Explicit merchant intent required. High risk.",
		},
		{
			Operation: OpRegisterExistingPage, Status: StatusImplemented, ExecutionClass: ClassDeterministic,
			ExistingPage: true, NewIdentity: false,
			Mutations:     MutationFlags{PagesJSON: true, PreserveID: true},
			DeepSeek:      DeepSeekForbidden,
			BuilderPlanOp: "register_existing_page",
			Summary:       "Register an on-disk liquid that lacks a pages.json row. No content gen. No nav change.",
		},
		{
			Operation: OpUnregisterPage, Status: StatusContractDefinedNotImplemented, ExecutionClass: ClassDeterministic,
			ExistingPage: true, NewIdentity: false,
			Mutations: MutationFlags{PagesJSON: true, PreserveID: true},
			DeepSeek:  DeepSeekForbidden,
			Summary:   "Remove registry row; preserve page file. Distinct from delete.",
		},
		{
			Operation: OpAddToNavigation, Status: StatusImplemented, ExecutionClass: ClassDeterministic,
			ExistingPage: true, NewIdentity: false,
			Mutations:     MutationFlags{DefaultsJSON: true, PreserveID: true},
			DeepSeek:      DeepSeekForbidden,
			BuilderPlanOp: "add_to_navigation",
			Summary:       "Structured merge into defaults.json menu.items[]. Never full-file model rewrite.",
		},
		{
			Operation: OpRemoveFromNavigation, Status: StatusContractDefinedNotImplemented, ExecutionClass: ClassDeterministic,
			ExistingPage: true, NewIdentity: false,
			Mutations: MutationFlags{DefaultsJSON: true, PreserveID: true},
			DeepSeek:  DeepSeekForbidden,
			Summary:   "Remove one menu item; no pages.json or liquid deletion.",
		},
		{
			Operation: OpDuplicatePage, Status: StatusContractDefinedNotImplemented, ExecutionClass: ClassModelAssisted,
			ExistingPage: true, NewIdentity: true,
			Mutations: MutationFlags{PageFile: true, PagesJSON: true, NewIdentity: true},
			DeepSeek:  DeepSeekOptional,
			Summary:   "Copy existing page to a NEW unique identity. Nav explicit.",
		},
		{
			Operation: OpDiagnoseExistingPage, Status: StatusImplemented, ExecutionClass: ClassDeterministic,
			ExistingPage: true, NewIdentity: false,
			Mutations:     MutationFlags{PreserveID: true}, // may stage registry fix → then Partial
			DeepSeek:      DeepSeekForbidden,
			BuilderPlanOp: "diagnose_existing_page",
			Summary:       "Deterministic structural diagnosis of file/registry/template/nav relationship.",
		},
		{
			Operation: OpFixExistingPage, Status: StatusPartial, ExecutionClass: ClassModelAssisted,
			ExistingPage: true, NewIdentity: false,
			Mutations:     MutationFlags{PageFile: true, PagesJSON: true, DefaultsJSON: true, PreserveID: true},
			DeepSeek:      DeepSeekOptional,
			BuilderPlanOp: "fix_existing_page",
			Summary:       "Diagnose first; deterministic fix when safe; otherwise focused model repair. No unrelated files.",
		},
		{
			Operation: OpClarify, Status: StatusImplemented, ExecutionClass: ClassDeterministic,
			ExistingPage: false, NewIdentity: false,
			Mutations:     MutationFlags{},
			DeepSeek:      DeepSeekNA,
			BuilderPlanOp: "clarify",
			Summary:       "Ask merchant for clarification; no file mutations.",
		},
	}
}

// RuleFor returns the lifecycle rule for an operation, if known.
func RuleFor(op PageOperation) (LifecycleRule, bool) {
	for _, r := range AllLifecycleRules() {
		if r.Operation == op {
			return r, true
		}
	}
	return LifecycleRule{}, false
}

// CanonicalVocabulary returns every frozen PageOperation string (sorted by AllLifecycleRules order).
func CanonicalVocabulary() []PageOperation {
	rules := AllLifecycleRules()
	out := make([]PageOperation, len(rules))
	for i, r := range rules {
		out[i] = r.Operation
	}
	return out
}

// ImplementedBuilderPlanOps returns op strings that BuilderPlan may emit today.
func ImplementedBuilderPlanOps() []string {
	var out []string
	for _, r := range AllLifecycleRules() {
		if r.BuilderPlanOp != "" && (r.Status == StatusImplemented || r.Status == StatusPartial) {
			out = append(out, r.BuilderPlanOp)
		}
	}
	return out
}

// NotImplementedOps returns vocabulary marked CONTRACT_DEFINED_NOT_IMPLEMENTED.
func NotImplementedOps() []PageOperation {
	var out []PageOperation
	for _, r := range AllLifecycleRules() {
		if r.Status == StatusContractDefinedNotImplemented {
			out = append(out, r.Operation)
		}
	}
	return out
}

// ParsePageOperation validates a string against the frozen vocabulary.
func ParsePageOperation(s string) (PageOperation, bool) {
	op := PageOperation(s)
	_, ok := RuleFor(op)
	return op, ok
}

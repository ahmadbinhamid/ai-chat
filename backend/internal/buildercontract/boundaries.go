package buildercontract

// MLBoundary describes what local ML may and must not do.
type MLBoundary struct {
	MayAssist []string
	MustNot   []string
}

// LocalMLBoundary returns the frozen ML policy for contract_version=1.
func LocalMLBoundary() MLBoundary {
	return MLBoundary{
		MustNot: []string{
			"override pages.json structure",
			"override defaults.json structure",
			"invent page file path conventions",
			"change page/slug identity rules",
			"change registration rules",
			"change menu merge rules",
			"bypass compound gates for N>=2 creates",
			"override publish/draft rules",
			"override registry validation",
			"override deterministic diagnostics",
			"change canonical PageOperation kind",
			"emit CONTRACT_DEFINED_NOT_IMPLEMENTED ops as if executable",
		},
		MayAssist: []string{
			"semantic constraints (audience, tone, content preferences)",
			"protected-field language when deterministic rules cannot decide",
			"follow-up language → target hints (complement active_target)",
			"nuanced intent when deterministic classifier cannot safely decide",
			"SEO wording within update_seo_meta fields",
			"content quality for update/regenerate once op kind is fixed",
		},
	}
}

// DeepSeekContextKey selects which contract slice to pack into model context.
// Never inject the full markdown contract into every request.
type DeepSeekContextKey string

const (
	DeepSeekSliceCreate     DeepSeekContextKey = "create"
	DeepSeekSliceUpdate     DeepSeekContextKey = "update"
	DeepSeekSliceRegenerate DeepSeekContextKey = "regenerate"
	DeepSeekSliceRegister   DeepSeekContextKey = "register"
	DeepSeekSliceNavigation DeepSeekContextKey = "navigation"
	DeepSeekSliceDiagnose   DeepSeekContextKey = "diagnose"
	DeepSeekSliceFix        DeepSeekContextKey = "fix"
	DeepSeekSliceSEO        DeepSeekContextKey = "seo"
)

// DeepSeekSlice is a focused rule pack for model context.
type DeepSeekSlice struct {
	Key   DeepSeekContextKey
	Rules []string
}

// DeepSeekSlices returns operation-keyed contract slices (not full MD).
func DeepSeekSlices() []DeepSeekSlice {
	return []DeepSeekSlice{
		{Key: DeepSeekSliceCreate, Rules: []string{
			"create_page = new identity: pages/<slug>.liquid + pages.json entry",
			"custom: slug == page == basename",
			"prefer page_registry_entry over full pages.json rewrite",
			"status published when merchant expects live page; omitted status = draft → prod 404",
			"do not add navigation unless asked",
			"N>=2 pages → compound; one registry entry per file",
			"mandatory layout-start/end boilerplate",
		}},
		{Key: DeepSeekSliceUpdate, Rules: []string{
			"update_page_content preserves page/slug/path/type identity",
			"must not create a second page or new slug",
			"must not invent duplicate registry entries",
			"prefer action edit over full update when possible",
			"do not rewrite unrelated pages or defaults.json",
		}},
		{Key: DeepSeekSliceRegenerate, Rules: []string{
			"regenerate_page_content = rewrite EXISTING page body; same identity",
			"same slug, page key, route, registry identity",
			"must not create second page, delete/re-register identity, or duplicate nav",
			"if new identity wanted → recreate_page / create_page (explicit)",
			"NOTE: regenerate is CONTRACT_DEFINED_NOT_IMPLEMENTED as distinct executor; do not invent recreate semantics",
		}},
		{Key: DeepSeekSliceRegister, Rules: []string{
			"register_existing_page: file must already exist",
			"merge ONE registry entry; no content generation; no nav change",
			"idempotent if already registered",
			"DeepSeek must not run when deterministic path applies",
		}},
		{Key: DeepSeekSliceNavigation, Rules: []string{
			"add_to_navigation: defaults.json menu.items[] structured merge only",
			"never emit full defaults.json rewrite",
			"preserve existing items; reject duplicates",
			"registration ≠ navigation",
		}},
		{Key: DeepSeekSliceDiagnose, Rules: []string{
			"diagnose_existing_page is deterministic-first",
			"check file, registry, identity, route, template, deps, nav relationship",
			"no DeepSeek for basic diagnosis",
		}},
		{Key: DeepSeekSliceFix, Rules: []string{
			"fix_existing_page: diagnose first",
			"deterministic fix when safe (e.g. missing registration)",
			"focused DeepSeek repair only for content/template logic",
			"no unrelated files",
		}},
		{Key: DeepSeekSliceSEO, Rules: []string{
			"update_seo_meta only seo_title, seo_description, seo_keywords, og_*",
			"preserve unrequested SEO fields and slug/page identity",
			"structured registry update; never rewrite whole pages.json",
		}},
	}
}

// SliceForOperation maps a PageOperation to the DeepSeek context key (if any).
func SliceForOperation(op PageOperation) (DeepSeekContextKey, bool) {
	switch op {
	case OpCreatePage, OpRegisterPage:
		return DeepSeekSliceCreate, true
	case OpUpdatePageContent, OpFullPageEdit, OpSectionEdit, OpSimpleStyleEdit:
		return DeepSeekSliceUpdate, true
	case OpRegeneratePageContent:
		return DeepSeekSliceRegenerate, true
	case OpRegisterExistingPage, OpUnregisterPage:
		return DeepSeekSliceRegister, true
	case OpAddToNavigation, OpRemoveFromNavigation:
		return DeepSeekSliceNavigation, true
	case OpDiagnoseExistingPage:
		return DeepSeekSliceDiagnose, true
	case OpFixExistingPage:
		return DeepSeekSliceFix, true
	case OpUpdateSEOMeta:
		return DeepSeekSliceSEO, true
	default:
		return "", false
	}
}

// TrainingMetadata keys that every future builder example should include.
const (
	TrainingKeyContractVersion   = "contract_version"
	TrainingKeyOperation         = "operation"
	TrainingKeyPlanClassification = "plan_classification"
	TrainingKeySemanticRefinement = "semantic_refinement"
	TrainingKeyExecutionResult   = "execution_result"
)

// RequiredTrainingMetadata returns required example metadata field names.
func RequiredTrainingMetadata() []string {
	return []string{
		TrainingKeyContractVersion,
		TrainingKeyOperation,
		TrainingKeyPlanClassification,
		TrainingKeySemanticRefinement,
		TrainingKeyExecutionResult,
	}
}

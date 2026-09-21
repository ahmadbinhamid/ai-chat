package builderdataset

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"ai-chat/internal/builderexamples"
)

// GateThresholds are the ML-11/ML-13 training readiness mins (do not lower).
type GateThresholds struct {
	UsableTotal            int `json:"usable_total"`
	PositiveSemantic       int `json:"positive_semantic"`
	DistinctSemanticGroups int `json:"distinct_semantic_groups"`
	ProtectedFields        int `json:"protected_fields"`
	Preferences            int `json:"preferences"`
	Clarification          int `json:"clarification"`
	SEORelated             int `json:"seo_related"`
	Compound               int `json:"compound"`
}

// DefaultGateThresholds returns the locked collection campaign thresholds.
func DefaultGateThresholds() GateThresholds {
	return GateThresholds{
		UsableTotal:            500,
		PositiveSemantic:       200,
		DistinctSemanticGroups: 100,
		ProtectedFields:        50,
		Preferences:            50,
		Clarification:          30,
		SEORelated:             40,
		Compound:               40,
	}
}

// ProgressLine is one readiness metric with have/need.
type ProgressLine struct {
	Name     string `json:"name"`
	Have     int    `json:"have"`
	Required int    `json:"required"`
	Missing  int    `json:"missing"`
}

// DiversityCounts tracks intent/routing diversity (no prompts).
type DiversityCounts struct {
	IntentSimpleEdit         int `json:"intent_simple_edit"`
	IntentSectionEdit        int `json:"intent_section_edit"`
	IntentFullPage           int `json:"intent_full_page"`
	IntentPageCreate         int `json:"intent_page_create"`
	IntentSEOMeta            int `json:"intent_seo_meta"`
	IntentCompound           int `json:"intent_compound"`
	IntentNavigationRegistry int `json:"intent_navigation_registry"`
	IntentAmbiguous          int `json:"intent_ambiguous"`
	IntentOther              int `json:"intent_other"`

	WithProtectedFields int `json:"with_protected_fields"`
	WithPreferences     int `json:"with_preferences"`
	WithClarification   int `json:"with_clarification"`
	SEORelated          int `json:"seo_related"`
	Compound            int `json:"compound"`

	DeterministicOps   int `json:"deterministic_operations"`
	LocalLMUsed        int `json:"local_lm_used"`
	LocalLMSkipped     int `json:"local_lm_skipped"`
	RefinementApplied  int `json:"refinement_applied"`
	RefinementRejected int `json:"refinement_rejected"`
	DeepSeekUsed       int `json:"deepseek_used"`
}

// DuplicateStats summarizes repetition without exposing prompts.
type DuplicateStats struct {
	SourceRows              int     `json:"source_rows"`
	ExactFingerprintGroups  int     `json:"exact_fingerprint_groups"`
	ExactDuplicateExtraRows int     `json:"exact_duplicate_extra_rows"`
	SemanticGroups          int     `json:"semantic_groups"`
	SemanticDuplicateExtras int     `json:"semantic_duplicate_extra_rows"`
	UniqueSemanticGroups    int     `json:"unique_normalized_request_groups"`
	DuplicateCollapseRate   float64 `json:"duplicate_collapse_rate"`
	TransformCollapsed      int     `json:"transform_collapsed"`
}

// ReadinessReport is the ML-13 dataset readiness view (no raw prompts).
type ReadinessReport struct {
	DatasetVersion string `json:"dataset_version"`
	GeneratedAt    string `json:"generated_at"`
	Scope          string `json:"scope"` // tenant:<id> | file | split-dir | global-cli
	TenantID       uint64 `json:"tenant_id,omitempty"`

	StoredTotal int `json:"stored_total"`
	Usable      int `json:"usable"`
	Excluded    int `json:"excluded"`

	PositiveSemantic int `json:"positive_semantic"`
	PositiveRouting  int `json:"positive_routing"`
	NegativeEval     int `json:"negative_eval"`
	AmbiguousEval    int `json:"ambiguous_eval"`

	DistinctSemanticGroups int `json:"distinct_semantic_groups"`

	OldestCreatedAt string `json:"oldest_created_at,omitempty"`
	NewestCreatedAt string `json:"newest_created_at,omitempty"`

	TenantDistribution map[string]int `json:"tenant_distribution,omitempty"`

	Diversity  DiversityCounts `json:"diversity"`
	Duplicates DuplicateStats  `json:"duplicates"`

	Thresholds       GateThresholds `json:"thresholds"`
	Progress         []ProgressLine `json:"progress"`
	Underrepresented []string       `json:"underrepresented_categories,omitempty"`
	GateStatus       string         `json:"gate_status"`
	GateReady        bool           `json:"gate_ready"`
	MissingCounts    map[string]int `json:"missing_counts,omitempty"`
	Recommendation   string         `json:"recommendation"`
	DatasetHash      string         `json:"dataset_hash,omitempty"`
}

type internalCounts struct {
	withProtected     int
	withPreferences   int
	withClarification int
	seoRelated        int
	compound          int
	semanticKeys      map[string]bool
}

// BuildReadinessFromSource transforms examples and builds a readiness report.
func BuildReadinessFromSource(source []builderexamples.Example, scope string, tenantID uint64) ReadinessReport {
	result := Transform(source)
	rep := BuildReadinessFromTransform(result, scope, tenantID)
	rep.StoredTotal = len(source)
	rep.OldestCreatedAt, rep.NewestCreatedAt = createdRange(source)
	if tenantID == 0 && scope == "global-cli" {
		rep.TenantDistribution = tenantDist(source)
	}
	rep.Duplicates = duplicateStats(source, result)
	return rep
}

// BuildReadinessFromTransform builds readiness from an already-transformed set.
func BuildReadinessFromTransform(result TransformResult, scope string, tenantID uint64) ReadinessReport {
	all := append(append(append([]TrainingExample{}, result.Train...), result.Validation...), result.Test...)
	ic := countInternal(all)
	th := DefaultGateThresholds()
	div := diversityFrom(all)

	need := map[string]int{}
	progress := []ProgressLine{
		prog("usable_total", result.Report.Usable, th.UsableTotal, need),
		prog("positive_semantic", result.Report.PositiveSemantic, th.PositiveSemantic, need),
		prog("distinct_semantic_groups", len(ic.semanticKeys), th.DistinctSemanticGroups, need),
		prog("protected_fields", ic.withProtected, th.ProtectedFields, need),
		prog("preferences", ic.withPreferences, th.Preferences, need),
		prog("clarification", ic.withClarification, th.Clarification, need),
		prog("seo_related", ic.seoRelated, th.SEORelated, need),
		prog("compound", ic.compound, th.Compound, need),
	}
	ready := len(need) == 0
	status := "READY_FOR_TRAINING"
	rec := "Dataset meets thresholds. Training is a separate phase — do not train from this command."
	if !ready {
		status = "NOT_READY_FOR_TRAINING"
		rec = "Collect more REAL builder executions in dev/staging (BUILDER_TRAINING_DATA_ENABLED=true). Do not fabricate data."
	}

	rep := ReadinessReport{
		DatasetVersion:         DatasetVersion,
		GeneratedAt:            time.Now().UTC().Format(time.RFC3339),
		Scope:                  scope,
		TenantID:               tenantID,
		Usable:                 result.Report.Usable,
		Excluded:               result.Report.Excluded,
		PositiveSemantic:       result.Report.PositiveSemantic,
		PositiveRouting:        result.Report.PositiveRouting,
		NegativeEval:           result.Report.NegativeEval,
		AmbiguousEval:          result.Report.AmbiguousEval,
		DistinctSemanticGroups: len(ic.semanticKeys),
		Diversity:              div,
		Thresholds:             th,
		Progress:               progress,
		Underrepresented:       underrepresented(div, th, ic),
		GateStatus:             status,
		GateReady:              ready,
		MissingCounts:          need,
		Recommendation:         rec,
		DatasetHash:            hashTraining(all),
		Duplicates: DuplicateStats{
			SemanticGroups:       len(ic.semanticKeys),
			UniqueSemanticGroups: len(ic.semanticKeys),
			TransformCollapsed:   result.Report.DuplicateCollapsed,
		},
	}
	if result.Report.TotalSource > 0 {
		rep.Duplicates.DuplicateCollapseRate = float64(result.Report.DuplicateCollapsed) / float64(result.Report.TotalSource)
	}
	return rep
}

func prog(name string, have, req int, need map[string]int) ProgressLine {
	miss := 0
	if have < req {
		miss = req - have
		need[name] = miss
	}
	return ProgressLine{Name: name, Have: have, Required: req, Missing: miss}
}

func countInternal(all []TrainingExample) internalCounts {
	ic := internalCounts{semanticKeys: map[string]bool{}}
	for _, ex := range all {
		ic.semanticKeys[ex.SemanticKey] = true
		if len(ex.Target.ProtectedFields) > 0 {
			ic.withProtected++
		}
		if len(ex.Target.Preferences) > 0 {
			ic.withPreferences++
		}
		if ex.Target.NeedsClarification || ex.Label == QualityAmbiguousEval {
			ic.withClarification++
		}
		if isSEOTraining(ex) {
			ic.seoRelated++
		}
		if ex.Metadata.Compound || ex.Input.Intent == "compound" {
			ic.compound++
		}
	}
	return ic
}

func diversityFrom(all []TrainingExample) DiversityCounts {
	var d DiversityCounts
	for _, ex := range all {
		switch ex.Input.Intent {
		case "simple_edit":
			d.IntentSimpleEdit++
		case "section_edit":
			d.IntentSectionEdit++
		case "full_page":
			d.IntentFullPage++
		case "page_create":
			d.IntentPageCreate++
		case "seo_meta":
			d.IntentSEOMeta++
		case "compound":
			d.IntentCompound++
		case "navigation_registry":
			d.IntentNavigationRegistry++
		case "ambiguous":
			d.IntentAmbiguous++
		default:
			d.IntentOther++
		}
		if len(ex.Target.ProtectedFields) > 0 {
			d.WithProtectedFields++
		}
		if len(ex.Target.Preferences) > 0 {
			d.WithPreferences++
		}
		if ex.Target.NeedsClarification || ex.Label == QualityAmbiguousEval {
			d.WithClarification++
		}
		if isSEOTraining(ex) {
			d.SEORelated++
		}
		if ex.Metadata.Compound || ex.Input.Intent == "compound" {
			d.Compound++
		}
		if ex.Metadata.DeterministicOp != "" {
			d.DeterministicOps++
		}
		if ex.Metadata.LocalLMUsed {
			d.LocalLMUsed++
		}
		if ex.Metadata.LocalLMSkipped {
			d.LocalLMSkipped++
		}
		if ex.Metadata.RefinementApplied {
			d.RefinementApplied++
		}
		if ex.Metadata.RefinementRejected {
			d.RefinementRejected++
		}
		if ex.Metadata.DeepSeekUsed {
			d.DeepSeekUsed++
		}
	}
	return d
}

func isSEOTraining(ex TrainingExample) bool {
	if ex.Input.Intent == "seo_meta" {
		return true
	}
	for _, op := range ex.Input.Operations {
		if op == "update_seo_meta" {
			return true
		}
	}
	for _, f := range ex.Target.ProtectedFields {
		switch f {
		case "meta_title", "meta_description", "seo_keywords", "og_title", "og_description":
			return true
		}
	}
	return false
}

func underrepresented(d DiversityCounts, th GateThresholds, ic internalCounts) []string {
	var out []string
	note := func(label string, have, softMin int) {
		if have < softMin {
			out = append(out, fmt.Sprintf("%s: have=%d soft_min=%d", label, have, softMin))
		}
	}
	note("simple_edit", d.IntentSimpleEdit, 20)
	note("section_edit", d.IntentSectionEdit, 15)
	note("page_create", d.IntentPageCreate, 15)
	note("seo_meta", d.IntentSEOMeta, 20)
	note("compound", d.IntentCompound, th.Compound)
	note("navigation_registry", d.IntentNavigationRegistry, 10)
	note("clarification/ambiguous", d.IntentAmbiguous, th.Clarification)
	note("protected_fields", ic.withProtected, th.ProtectedFields)
	note("preferences", ic.withPreferences, th.Preferences)
	note("deterministic_operations", d.DeterministicOps, 10)
	note("local_lm_refinement", d.RefinementApplied, 20)
	note("deepseek_generation", d.DeepSeekUsed, 50)
	sort.Strings(out)
	return out
}

func createdRange(source []builderexamples.Example) (oldest, newest string) {
	var o, n time.Time
	for _, ex := range source {
		if ex.CreatedAt.IsZero() {
			continue
		}
		if o.IsZero() || ex.CreatedAt.Before(o) {
			o = ex.CreatedAt
		}
		if n.IsZero() || ex.CreatedAt.After(n) {
			n = ex.CreatedAt
		}
	}
	if !o.IsZero() {
		oldest = o.UTC().Format(time.RFC3339)
	}
	if !n.IsZero() {
		newest = n.UTC().Format(time.RFC3339)
	}
	return
}

func tenantDist(source []builderexamples.Example) map[string]int {
	m := map[string]int{}
	for _, ex := range source {
		m[fmt.Sprintf("%d", ex.TenantID)]++
	}
	return m
}

func duplicateStats(source []builderexamples.Example, result TransformResult) DuplicateStats {
	fp := map[string]int{}
	for _, ex := range source {
		if ex.PromptFingerprint == "" {
			continue
		}
		fp[ex.PromptFingerprint]++
	}
	exactExtra := 0
	for _, c := range fp {
		if c > 1 {
			exactExtra += c - 1
		}
	}
	sem := map[string]int{}
	all := append(append(append([]TrainingExample{}, result.Train...), result.Validation...), result.Test...)
	for _, ex := range all {
		if ex.SemanticKey == "" {
			continue
		}
		sem[ex.SemanticKey]++
	}
	semExtra := 0
	for _, c := range sem {
		if c > 1 {
			semExtra += c - 1
		}
	}
	ds := DuplicateStats{
		SourceRows:              len(source),
		ExactFingerprintGroups:  len(fp),
		ExactDuplicateExtraRows: exactExtra,
		SemanticGroups:          len(sem),
		SemanticDuplicateExtras: semExtra,
		UniqueSemanticGroups:    len(sem),
		TransformCollapsed:      result.Report.DuplicateCollapsed,
	}
	if len(source) > 0 {
		ds.DuplicateCollapseRate = float64(result.Report.DuplicateCollapsed) / float64(len(source))
	}
	return ds
}

func hashTraining(examples []TrainingExample) string {
	type row struct {
		K string `json:"k"`
		L string `json:"l"`
		I string `json:"i"`
	}
	rows := make([]row, 0, len(examples))
	for _, ex := range examples {
		rows = append(rows, row{K: ex.SemanticKey, L: string(ex.Label), I: ex.Input.Intent})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].K != rows[j].K {
			return rows[i].K < rows[j].K
		}
		return rows[i].L < rows[j].L
	})
	b, _ := json.Marshal(rows)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// FormatReadinessText renders the human checklist-style status (no prompts).
func FormatReadinessText(r ReadinessReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "dataset=%s scope=%s gate=%s\n", r.DatasetVersion, r.Scope, r.GateStatus)
	if r.TenantID != 0 {
		fmt.Fprintf(&b, "tenant_id=%d\n", r.TenantID)
	}
	fmt.Fprintf(&b, "stored_total=%d excluded=%d\n", r.StoredTotal, r.Excluded)
	if r.OldestCreatedAt != "" || r.NewestCreatedAt != "" {
		fmt.Fprintf(&b, "age_range=%s .. %s\n", r.OldestCreatedAt, r.NewestCreatedAt)
	}
	for _, p := range r.Progress {
		fmt.Fprintf(&b, "%s: %d / %d", strings.ToUpper(p.Name), p.Have, p.Required)
		if p.Missing > 0 {
			fmt.Fprintf(&b, " (missing %d)", p.Missing)
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "POSITIVE_ROUTING: %d\n", r.PositiveRouting)
	fmt.Fprintf(&b, "NEGATIVE_EVAL: %d\n", r.NegativeEval)
	fmt.Fprintf(&b, "AMBIGUOUS_EVAL: %d\n", r.AmbiguousEval)
	fmt.Fprintf(&b, "DUPLICATES: fingerprint_groups=%d exact_extra=%d semantic_groups=%d collapse_rate=%.2f\n",
		r.Duplicates.ExactFingerprintGroups, r.Duplicates.ExactDuplicateExtraRows,
		r.Duplicates.UniqueSemanticGroups, r.Duplicates.DuplicateCollapseRate)
	fmt.Fprintf(&b, "SIGNALS: deepseek=%d local_lm=%d local_skipped=%d refine_ok=%d refine_rej=%d deterministic=%d\n",
		r.Diversity.DeepSeekUsed, r.Diversity.LocalLMUsed, r.Diversity.LocalLMSkipped,
		r.Diversity.RefinementApplied, r.Diversity.RefinementRejected, r.Diversity.DeterministicOps)
	if len(r.Underrepresented) > 0 {
		b.WriteString("UNDERREPRESENTED:\n")
		for _, u := range r.Underrepresented {
			fmt.Fprintf(&b, "  - %s\n", u)
		}
	}
	if len(r.TenantDistribution) > 0 {
		b.WriteString("TENANT_DISTRIBUTION (counts only):\n")
		keys := make([]string, 0, len(r.TenantDistribution))
		for k := range r.TenantDistribution {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "  tenant_%s=%d\n", k, r.TenantDistribution[k])
		}
	}
	fmt.Fprintf(&b, "recommendation: %s\n", r.Recommendation)
	return b.String()
}

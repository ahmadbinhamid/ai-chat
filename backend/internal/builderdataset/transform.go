package builderdataset

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"ai-chat/internal/builderexamples"
	"ai-chat/internal/builderplan"
)

var (
	tokenLikeRe = regexp.MustCompile(`(?i)\b(bearer\s+[a-z0-9\-._~+/]+=*|sk-[a-z0-9]{10,}|api[_-]?key\s*[:=]\s*\S+|password\s*[:=]\s*\S+|authorization\s*[:=]\s*\S+)\b`)
	emailRe     = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)
	credURLRe   = regexp.MustCompile(`(?i)(https?://)([^/\s:@]+):([^/\s@]+)@`)
	tenantIDRe  = regexp.MustCompile(`(?i)\b(tenant[_-]?id|user[_-]?id|chat[_-]?id|generation[_-]?id)\s*[:=]\s*\S+`)
	hostPathRe  = regexp.MustCompile(`(?i)(/Applications/|/Users/|/home/|\\\\|C:\\|localhost:\d+|127\.0\.0\.1:\d+)`)
)

// knownIntents / knownOps — BuilderPlan vocabulary only.
var knownIntents = map[string]bool{
	string(builderplan.IntentSimpleEdit): true, string(builderplan.IntentSectionEdit): true,
	string(builderplan.IntentFullPage): true, string(builderplan.IntentCompound): true,
	string(builderplan.IntentSEOMeta): true, string(builderplan.IntentPageCreate): true,
	string(builderplan.IntentNavigationRegistry): true, string(builderplan.IntentAmbiguous): true,
}

var knownOps = map[string]bool{
	string(builderplan.OpSimpleStyleEdit): true, string(builderplan.OpSectionEdit): true,
	string(builderplan.OpFullPageEdit): true, string(builderplan.OpCreatePage): true,
	string(builderplan.OpRegisterPage): true, string(builderplan.OpRegisterExistingPage): true,
	string(builderplan.OpUpdateSEOMeta): true, string(builderplan.OpUpdatePageContent): true,
	string(builderplan.OpAddToNavigation): true, string(builderplan.OpClarify): true,
}

// ExclusionReason explains why a source example was dropped.
type ExclusionReason string

const (
	ExcludeEmptyInput        ExclusionReason = "empty_input"
	ExcludeEmptyTarget       ExclusionReason = "empty_target_for_refinement"
	ExcludeMalformed         ExclusionReason = "malformed"
	ExcludeInvalidIntent     ExclusionReason = "invalid_intent"
	ExcludeInvalidOperation  ExclusionReason = "invalid_operation"
	ExcludeUnknownOutcome    ExclusionReason = "unknown_outcome"
	ExcludeSensitiveResidual ExclusionReason = "sensitive_residual"
	ExcludeInconsistent      ExclusionReason = "internally_inconsistent"
	ExcludeDuplicate         ExclusionReason = "duplicate_semantic"
	ExcludeNoUsableSignal    ExclusionReason = "no_usable_signal"
)

// TransformResult is the cleaned dataset plus quality counters.
type TransformResult struct {
	Version    string
	Train      []TrainingExample
	Validation []TrainingExample
	Test       []TrainingExample
	Excluded   []ExcludedRecord
	Report     QualityReport
}

// ExcludedRecord keeps an exclusion counter trail (no tenant IDs).
type ExcludedRecord struct {
	Reason            ExclusionReason `json:"reason"`
	PromptFingerprint string          `json:"prompt_fingerprint,omitempty"`
	Intent            string          `json:"intent,omitempty"`
	Category          string          `json:"outcome_category,omitempty"`
}

// QualityReport is the dataset quality summary.
type QualityReport struct {
	DatasetVersion             string         `json:"dataset_version"`
	TotalSource                int            `json:"total_source"`
	Usable                     int            `json:"usable"`
	Excluded                   int            `json:"excluded"`
	ExclusionReasons           map[string]int `json:"exclusion_reasons"`
	PositiveSemantic           int            `json:"positive_semantic"`
	PositiveRouting            int            `json:"positive_routing"`
	NegativeEval               int            `json:"negative_eval"`
	AmbiguousEval              int            `json:"ambiguous_eval"`
	TrainCount                 int            `json:"train_count"`
	ValidationCount            int            `json:"validation_count"`
	TestCount                  int            `json:"test_count"`
	DuplicateCollapsed         int            `json:"duplicate_collapsed"`
	SanitizedPromptUsed        int            `json:"sanitized_prompt_used"`
	IntentDistribution         map[string]int `json:"intent_distribution"`
	OperationDistribution      map[string]int `json:"operation_distribution"`
	ConstraintDistribution     map[string]int `json:"constraint_distribution"`
	ProtectedFieldDistribution map[string]int `json:"protected_field_distribution"`
	PreferenceDistribution     map[string]int `json:"preference_distribution"`
	ComplexityDistribution     map[string]int `json:"complexity_distribution"`
	SourceDistribution         map[string]int `json:"source_distribution"`
	ImbalanceNotes             []string       `json:"imbalance_notes,omitempty"`
}

// Transform converts source ML-9 examples into a reproducible dataset.
// Deterministic given the same source slice order is normalized first.
func Transform(source []builderexamples.Example) TransformResult {
	report := QualityReport{
		DatasetVersion:             DatasetVersion,
		TotalSource:                len(source),
		ExclusionReasons:           map[string]int{},
		IntentDistribution:         map[string]int{},
		OperationDistribution:      map[string]int{},
		ConstraintDistribution:     map[string]int{},
		ProtectedFieldDistribution: map[string]int{},
		PreferenceDistribution:     map[string]int{},
		ComplexityDistribution:     map[string]int{},
		SourceDistribution:         map[string]int{},
	}

	// Deterministic source order: fingerprint, then created_at, then id.
	sorted := append([]builderexamples.Example(nil), source...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].PromptFingerprint != sorted[j].PromptFingerprint {
			return sorted[i].PromptFingerprint < sorted[j].PromptFingerprint
		}
		if !sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
		}
		return sorted[i].ID < sorted[j].ID
	})

	var usable []TrainingExample
	var excluded []ExcludedRecord
	seenSemantic := map[string]bool{}

	for _, ex := range sorted {
		te, reason, ok := convertOne(ex)
		if !ok {
			report.ExclusionReasons[string(reason)]++
			excluded = append(excluded, ExcludedRecord{
				Reason:            reason,
				PromptFingerprint: ex.PromptFingerprint,
				Intent:            ex.Intent,
				Category:          string(ex.Outcome.Category),
			})
			continue
		}
		if seenSemantic[te.SemanticKey] {
			report.DuplicateCollapsed++
			report.ExclusionReasons[string(ExcludeDuplicate)]++
			excluded = append(excluded, ExcludedRecord{
				Reason:            ExcludeDuplicate,
				PromptFingerprint: ex.PromptFingerprint,
				Intent:            te.Input.Intent,
				Category:          string(ex.Outcome.Category),
			})
			continue
		}
		seenSemantic[te.SemanticKey] = true
		usable = append(usable, te)
		tallyExample(&report, te)
	}

	report.Usable = len(usable)
	report.Excluded = len(excluded)
	train, val, test := splitDeterministic(usable)
	for i := range train {
		train[i].Split = "train"
	}
	for i := range val {
		val[i].Split = "validation"
	}
	for i := range test {
		test[i].Split = "test"
	}
	report.TrainCount = len(train)
	report.ValidationCount = len(val)
	report.TestCount = len(test)
	report.ImbalanceNotes = imbalanceNotes(report)

	return TransformResult{
		Version:    DatasetVersion,
		Train:      train,
		Validation: val,
		Test:       test,
		Excluded:   excluded,
		Report:     report,
	}
}

func convertOne(ex builderexamples.Example) (TrainingExample, ExclusionReason, bool) {
	if ex.PromptFingerprint == "" && strings.TrimSpace(ex.PromptSanitized) == "" {
		return TrainingExample{}, ExcludeEmptyInput, false
	}
	if ex.Outcome.Category == "" && !ex.Outcome.Success && !ex.Outcome.Failed && !ex.Outcome.Partial {
		return TrainingExample{}, ExcludeUnknownOutcome, false
	}

	intent := normalizeIntent(ex.Intent)
	if intent == "" || !knownIntents[intent] {
		return TrainingExample{}, ExcludeInvalidIntent, false
	}
	ops := normalizeOperations(ex.Operations)
	if len(ops) == 0 {
		return TrainingExample{}, ExcludeInvalidOperation, false
	}
	for _, op := range ops {
		if !knownOps[op] {
			return TrainingExample{}, ExcludeInvalidOperation, false
		}
	}

	prompt, _, sens := sanitizeForTraining(ex.PromptSanitized)
	if sens {
		return TrainingExample{}, ExcludeSensitiveResidual, false
	}

	protected := canonicalizeProtected(ex.ProtectedFields)
	prefs := canonicalizePreferences(ex.Preferences)
	constraints := canonicalizeConstraints(ex.Constraints, protected, prefs)

	// Inconsistency: training_positive but failed, etc.
	if ex.Outcome.TrainingPositive && ex.Outcome.Failed {
		return TrainingExample{}, ExcludeInconsistent, false
	}
	if ex.Outcome.Success && ex.Outcome.Failed {
		return TrainingExample{}, ExcludeInconsistent, false
	}

	label, failCat, source := labelExample(ex, protected, prefs, constraints)
	if label == QualityExcluded {
		return TrainingExample{}, ExcludeNoUsableSignal, false
	}

	needsClarify := intent == string(builderplan.IntentAmbiguous) ||
		ex.Outcome.Category == builderexamples.OutcomeAmbiguous ||
		containsOp(ops, string(builderplan.OpClarify))

	target := TrainingTarget{
		Constraints:        constraints,
		ProtectedFields:    protected,
		Preferences:        prefs,
		NeedsClarification: needsClarify,
	}

	// Semantic positives must have a non-empty refinement target.
	if label == QualityPositiveSemantic && !hasRefinementTarget(target) {
		return TrainingExample{}, ExcludeEmptyTarget, false
	}

	in := TrainingInput{
		Prompt:              prompt,
		PromptFingerprint:   ex.PromptFingerprint,
		Intent:              intent,
		Operations:          ops,
		ExistingConstraints: plannerBaselineConstraints(ex.Constraints),
	}

	meta := TrainingMetadata{
		Complexity:        normalizeComplexity(ex.Complexity),
		Compound:          ex.Compound || intent == string(builderplan.IntentCompound),
		ContextCategories: contextCategories(ex.Context),
		Success:           ex.Outcome.Success && !ex.Outcome.Partial && !ex.Outcome.Failed,
		Source:            source,
		FailureCategory:   string(failCat),
		LocalLMUsed:        ex.Execution.LocalLMUsed,
		LocalLMSkipped:     ex.Execution.LocalLMSkipped,
		RefinementApplied:  ex.Execution.RefinementApplied,
		RefinementRejected: ex.Execution.RefinementRejected,
		DeepSeekUsed:       ex.Execution.DeepSeekUsed,
		DeterministicOp:    ex.Execution.DeterministicOperation,
	}

	te := TrainingExample{
		DatasetVersion: DatasetVersion,
		Input:          in,
		Target:         target,
		Metadata:       meta,
		Label:          label,
		SemanticKey:    semanticKey(in, target, label),
	}
	return te, "", true
}

func labelExample(ex builderexamples.Example, protected, prefs, constraints []string) (Quality, FailureCategory, string) {
	cat := ex.Outcome.Category
	switch cat {
	case builderexamples.OutcomeCancelled:
		return QualityNegativeEval, FailCancelled, "failure"
	case builderexamples.OutcomeProviderTimeout:
		return QualityNegativeEval, FailProviderTimeout, "failure"
	case builderexamples.OutcomeProviderError:
		return QualityNegativeEval, FailProviderError, "failure"
	case builderexamples.OutcomeValidationFailure:
		return QualityNegativeEval, FailValidationFailure, "failure"
	case builderexamples.OutcomePartialSuccess:
		return QualityNegativeEval, FailPartialSuccess, "failure"
	case builderexamples.OutcomeAmbiguous:
		return QualityAmbiguousEval, FailAmbiguousRequest, "ambiguous"
	case builderexamples.OutcomeUnknownFailure:
		return QualityNegativeEval, FailUnknown, "failure"
	}

	if ex.Outcome.Failed {
		if ex.Execution.DeterministicOperation != "" {
			return QualityNegativeEval, FailDeterministicOpFailure, "failure"
		}
		return QualityNegativeEval, FailUnknown, "failure"
	}

	// Clean successes
	if !ex.Outcome.TrainingPositive && !ex.Outcome.Success {
		return QualityExcluded, FailNone, "failure"
	}
	if ex.Outcome.Partial {
		return QualityNegativeEval, FailPartialSuccess, "failure"
	}

	hasSem := len(protected) > 0 || len(prefs) > 0 || hasSemanticConstraint(constraints)
	if ex.Outcome.TrainingPositive && hasSem && (ex.Execution.LocalLMUsed || hasSem) {
		// Prefer semantic positives when refinement signal exists and run succeeded.
		// LocalLMUsed preferred but protected-field successes without LM still count
		// if constraints were recorded (heuristic path).
		source := "local_lm"
		if !ex.Execution.LocalLMUsed {
			source = "deepseek"
		}
		if ex.Execution.DeterministicOperation != "" && !ex.Execution.DeepSeekUsed {
			source = "deterministic"
		}
		return QualityPositiveSemantic, FailNone, source
	}

	if ex.Outcome.TrainingPositive || (ex.Outcome.Success && ex.Outcome.ValidationPassed) {
		source := "deepseek"
		if ex.Execution.DeterministicOperation != "" && !ex.Execution.DeepSeekUsed {
			source = "deterministic"
		} else if ex.Execution.LocalLMUsed {
			source = "local_lm"
		}
		return QualityPositiveRouting, FailNone, source
	}

	return QualityExcluded, FailNone, "failure"
}

func hasRefinementTarget(t TrainingTarget) bool {
	return len(t.Constraints) > 0 || len(t.ProtectedFields) > 0 || len(t.Preferences) > 0 || t.NeedsClarification
}

func hasSemanticConstraint(cs []string) bool {
	for _, c := range cs {
		low := strings.ToLower(c)
		if strings.HasPrefix(low, "protect_field:") || strings.HasPrefix(low, "preference:") {
			return true
		}
		if strings.Contains(low, "preserve") || strings.Contains(low, "software") ||
			strings.Contains(low, "audience") || strings.Contains(low, "jpro") {
			return true
		}
	}
	return false
}

func normalizeIntent(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	return s
}

func normalizeComplexity(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "low", "medium", "high":
		return s
	default:
		return ""
	}
}

func normalizeOperations(ops []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ops))
	for _, op := range ops {
		op = strings.TrimSpace(strings.ToLower(op))
		// Alias legacy names if any appear.
		switch op {
		case "update_content":
			op = string(builderplan.OpUpdatePageContent)
		case "register_existing":
			op = string(builderplan.OpRegisterExistingPage)
		}
		if op == "" || seen[op] {
			continue
		}
		seen[op] = true
		out = append(out, op)
	}
	sort.Strings(out)
	return out
}

func canonicalizeProtected(fields []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = canonicalProtectedField(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// canonicalProtectedField maps aliases onto BuilderPlan protect_field vocabulary.
func canonicalProtectedField(f string) string {
	f = strings.ToLower(strings.TrimSpace(f))
	f = strings.ReplaceAll(f, " ", "_")
	f = strings.TrimPrefix(f, "protect_field:")
	switch f {
	case "jpro_meta_title", "jpro_meta_titles", "seo_title", "title", "meta_titles":
		return "meta_title"
	case "seo_description", "description":
		return "meta_description"
	case "seo_keywords", "keywords":
		return "seo_keywords"
	case "og_image_path", "og_image":
		return "og_image"
	case "meta_title", "meta_description", "slug", "path", "status",
		"og_title", "og_description", "pricing":
		return f
	default:
		for _, r := range f {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
				return ""
			}
		}
		if len(f) == 0 || len(f) > 40 {
			return ""
		}
		return f
	}
}

func canonicalizePreferences(prefs []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(prefs))
	for _, p := range prefs {
		p = strings.TrimSpace(strings.ToLower(p))
		p = strings.TrimPrefix(p, "preference:")
		p = strings.ReplaceAll(p, " ", "_")
		switch {
		case strings.Contains(p, "software_house") || strings.Contains(p, "software_company") || strings.Contains(p, "saas"):
			p = "software_house_audience"
		case strings.Contains(p, "professional"):
			p = "professional_tone"
		case strings.Contains(p, "modern"):
			p = "modern_style"
		}
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func canonicalizeConstraints(raw []string, protected, prefs []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(raw)+len(protected)+len(prefs))
	add := func(c string) {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			return
		}
		seen[c] = true
		out = append(out, c)
	}
	for _, f := range protected {
		add("protect_field:" + f)
	}
	for _, p := range prefs {
		add("preference:" + p)
	}
	for _, c := range raw {
		c = strings.TrimSpace(c)
		low := strings.ToLower(c)
		// Map free-text protect/keep meta phrasings.
		if strings.Contains(low, "jpro") && (strings.Contains(low, "meta") || strings.Contains(low, "title")) {
			add("protect_field:meta_title")
			continue
		}
		if (strings.Contains(low, "keep") || strings.Contains(low, "preserve") || strings.Contains(low, "don't change") || strings.Contains(low, "do not change")) &&
			(strings.Contains(low, "meta") || strings.Contains(low, "title")) {
			add("protect_field:meta_title")
			continue
		}
		if strings.Contains(low, "software house") || strings.Contains(low, "software company") {
			add("preference:software_house_audience")
			continue
		}
		if strings.HasPrefix(low, "protect_field:") {
			f := canonicalProtectedField(c)
			if f != "" {
				add("protect_field:" + f)
			}
			continue
		}
		if strings.HasPrefix(low, "preference:") {
			ps := canonicalizePreferences([]string{c})
			for _, p := range ps {
				add("preference:" + p)
			}
			continue
		}
		// Drop planner boilerplate from target constraints.
		if isPlannerBoilerplate(low) {
			continue
		}
		add(c)
	}
	sort.Strings(out)
	return out
}

func plannerBaselineConstraints(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, c := range raw {
		low := strings.ToLower(strings.TrimSpace(c))
		if isPlannerBoilerplate(low) {
			out = append(out, strings.TrimSpace(c))
		}
	}
	sort.Strings(out)
	return out
}

func isPlannerBoilerplate(low string) bool {
	return strings.HasPrefix(low, "never_") ||
		strings.HasPrefix(low, "prefer_") ||
		strings.HasPrefix(low, "planner_") ||
		strings.Contains(low, "existing_pages_remain") ||
		strings.Contains(low, "no_destructive")
}

func contextCategories(ctx builderexamples.ContextSnapshot) []string {
	var out []string
	if ctx.PrimaryCount > 0 || len(ctx.PrimaryFiles) > 0 {
		out = append(out, "primary")
	}
	if ctx.DependencyCount > 0 || len(ctx.DependencyFiles) > 0 {
		out = append(out, "dependency")
	}
	if ctx.SchemaCount > 0 || len(ctx.SchemaFiles) > 0 {
		out = append(out, "schema")
	}
	if ctx.ReferenceCount > 0 || len(ctx.ReferenceFiles) > 0 {
		out = append(out, "reference")
	}
	return out
}

func containsOp(ops []string, want string) bool {
	for _, op := range ops {
		if op == want {
			return true
		}
	}
	return false
}

func sanitizeForTraining(prompt string) (clean string, used bool, sensitive bool) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", false, false
	}
	// Credentials / auth URLs → exclude entirely (do not train on secret-bearing prompts).
	if tokenLikeRe.MatchString(prompt) || credURLRe.MatchString(prompt) {
		return "", false, true
	}
	s := emailRe.ReplaceAllString(prompt, "[EMAIL]")
	s = tenantIDRe.ReplaceAllString(s, "[REDACTED_ID]")
	s = hostPathRe.ReplaceAllString(s, "[PATH]")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) > 400 {
		s = string(runes[:400]) + "…"
	}
	return s, true, false
}

func semanticKey(in TrainingInput, t TrainingTarget, label Quality) string {
	parts := []string{
		in.Intent,
		strings.Join(in.Operations, ","),
		strings.Join(t.ProtectedFields, ","),
		strings.Join(t.Preferences, ","),
		strings.Join(t.Constraints, ","),
		fmt.Sprintf("clarify=%v", t.NeedsClarification),
		string(label),
	}
	if in.Prompt != "" {
		parts = append(parts, strings.Join(strings.Fields(strings.ToLower(in.Prompt)), " "))
	} else {
		parts = append(parts, in.PromptFingerprint)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:16])
}

func tallyExample(r *QualityReport, te TrainingExample) {
	switch te.Label {
	case QualityPositiveSemantic:
		r.PositiveSemantic++
	case QualityPositiveRouting:
		r.PositiveRouting++
	case QualityNegativeEval:
		r.NegativeEval++
	case QualityAmbiguousEval:
		r.AmbiguousEval++
	}
	if te.Input.Prompt != "" {
		r.SanitizedPromptUsed++
	}
	r.IntentDistribution[te.Input.Intent]++
	r.ComplexityDistribution[te.Metadata.Complexity]++
	r.SourceDistribution[te.Metadata.Source]++
	for _, op := range te.Input.Operations {
		r.OperationDistribution[op]++
	}
	for _, c := range te.Target.Constraints {
		r.ConstraintDistribution[c]++
	}
	for _, f := range te.Target.ProtectedFields {
		r.ProtectedFieldDistribution[f]++
	}
	for _, p := range te.Target.Preferences {
		r.PreferenceDistribution[p]++
	}
}

func imbalanceNotes(r QualityReport) []string {
	var notes []string
	if r.PositiveSemantic == 0 {
		notes = append(notes, "no positive_semantic examples — enable BUILDER_TRAINING_STORE_SANITIZED_PROMPT and local LM captures for refinement training")
	}
	if r.PositiveRouting > 0 && r.PositiveSemantic*5 < r.PositiveRouting {
		notes = append(notes, "positive_routing dominates positive_semantic — dataset skewed toward routing over refinement")
	}
	if r.AmbiguousEval == 0 {
		notes = append(notes, "no ambiguous_eval examples")
	}
	if r.NegativeEval == 0 {
		notes = append(notes, "no negative_eval examples")
	}
	return notes
}

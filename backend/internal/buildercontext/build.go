package buildercontext

import (
	"strings"
	"time"

	"ai-chat/internal/builderplan"
)

// Build derives a ContextPlan from a validated BuilderPlan. Pure — no I/O.
// Semantic constraints on the plan (protect_field:*, preference:*) influence
// schema/history only; they never invent arbitrary file paths.
func Build(plan builderplan.BuilderPlan) (ContextPlan, Metrics) {
	start := time.Now()
	meta := Metrics{}

	if plan.Ambiguous || plan.Intent == builderplan.IntentAmbiguous {
		cp := clarifyPlan()
		meta = metricsFrom(cp, start, 0)
		return cp, meta
	}
	if err := builderplan.Validate(plan); err != nil {
		cp := Fallback("validate_failed")
		meta = metricsFrom(cp, start, 0)
		meta.UsedFallback = true
		return cp, meta
	}

	cp := ContextPlan{
		MaxFiles:        DefaultMaxFiles,
		MaxReference:    DefaultMaxReference,
		MaxBytes:        DefaultMaxBytes,
		IncludeFileTree: true,
		Source:          "builderplan",
		Valid:           true,
	}

	candidates := builderplan.SelectContextFiles(plan)
	meta.Candidates = len(candidates)

	protectMeta := hasConstraintPrefix(plan.Constraints, "protect_field:meta") ||
		hasConstraintPrefix(plan.Constraints, "protect_field:seo") ||
		hasConstraintPrefix(plan.Constraints, "protect_field:jpro")
	needsSchema := false
	hasCreate := false
	hasContentEdit := false
	hasSimple := false
	hasSection := false
	hasNav := false
	hasSEO := false
	hasRegisterOnly := false

	for _, op := range plan.Operations {
		switch op.Kind {
		case builderplan.OpSimpleStyleEdit:
			hasSimple = true
			cp.PrimaryFiles = appendUnique(cp.PrimaryFiles, primaryForSimple(op.Target)...)
		case builderplan.OpSectionEdit:
			hasSection = true
			cp.PrimaryFiles = appendUnique(cp.PrimaryFiles, op.Target)
			cp.DependencyFiles = appendUnique(cp.DependencyFiles, dependencyForSection(op.Target)...)
		case builderplan.OpFullPageEdit, builderplan.OpUpdatePageContent:
			hasContentEdit = true
			cp.PrimaryFiles = appendUnique(cp.PrimaryFiles, op.Target)
			cp.DependencyFiles = appendUnique(cp.DependencyFiles, dependencyForPage(op.Target)...)
			if strings.Contains(strings.ToLower(op.Target), "blog") || protectMeta {
				needsSchema = true
			}
		case builderplan.OpCreatePage:
			hasCreate = true
			needsSchema = true
			cp.SchemaFiles = appendUnique(cp.SchemaFiles, "pages.json")
			cp.ReferenceFiles = appendUnique(cp.ReferenceFiles, "pages/blog.liquid", "pages/home.liquid")
		case builderplan.OpRegisterPage, builderplan.OpRegisterExistingPage:
			hasRegisterOnly = true
			needsSchema = true
			cp.SchemaFiles = appendUnique(cp.SchemaFiles, "pages.json")
		case builderplan.OpUpdateSEOMeta:
			hasSEO = true
			needsSchema = true
			cp.SchemaFiles = appendUnique(cp.SchemaFiles, "pages.json")
		case builderplan.OpAddToNavigation:
			hasNav = true
			cp.PrimaryFiles = appendUnique(cp.PrimaryFiles, "defaults.json")
			cp.DependencyFiles = appendUnique(cp.DependencyFiles, "components/header.liquid")
		case builderplan.OpClarify:
			return finalize(clarifyPlan(), start, meta.Candidates, 0)
		}
	}

	// Policy from the dominant operation class (not last-write-wins).
	switch {
	case hasSimple && !hasContentEdit && !hasCreate && !hasSection && !hasNav && !hasSEO:
		cp.OmitManifest = true
		cp.OmitFullPagesJSON = true
		cp.OmitFullDefaultsJSON = true
		cp.IncludeFileTree = true
		cp.History = HistoryPolicy{Focused: true, MaxRecentTurns: 0, SkipSummarize: true}
		cp.MaxFiles = 2
		cp.MaxBytes = 16_000
	case hasSection && !hasContentEdit && !hasCreate:
		cp.OmitManifest = true
		cp.OmitFullPagesJSON = true
		cp.History = HistoryPolicy{Focused: true, MaxRecentTurns: MinHistoryFocused, SkipSummarize: true}
		cp.MaxFiles = 4
		cp.MaxBytes = 24_000
	case hasCreate:
		cp.OmitManifest = true
		cp.OmitFullDefaultsJSON = true
		cp.History = HistoryPolicy{Focused: true, MaxRecentTurns: MinHistoryFocused, SkipSummarize: true}
		cp.MaxFiles = 6
		cp.MaxBytes = 28_000
	case hasContentEdit || hasSEO:
		cp.OmitManifest = true
		cp.History = HistoryPolicy{Focused: true, MaxRecentTurns: MinHistoryFocused, SkipSummarize: true}
		cp.MaxFiles = 6
		cp.MaxBytes = 32_000
	case hasNav:
		cp.OmitManifest = true
		cp.OmitFullPagesJSON = true
		cp.History = HistoryPolicy{Focused: true, MaxRecentTurns: MinHistoryFocused, SkipSummarize: true}
		cp.MaxFiles = 3
		cp.MaxBytes = 20_000
	case hasRegisterOnly:
		cp.OmitManifest = true
		cp.OmitFullDefaultsJSON = true
		cp.IncludeFileTree = false
		cp.History = HistoryPolicy{Focused: true, MaxRecentTurns: 0, SkipSummarize: true}
		cp.MaxFiles = 2
		cp.MaxBytes = 8_000
	default:
		cp.OmitManifest = true
		cp.History = HistoryPolicy{Focused: true, MaxRecentTurns: MaxHistoryFocused, SkipSummarize: true}
		cp.MaxFiles = DefaultMaxFiles
		cp.MaxBytes = DefaultMaxBytes
	}

	if needsSchema || protectMeta {
		cp.SchemaFiles = appendUnique(cp.SchemaFiles, "pages.json")
		cp.OmitFullPagesJSON = false
	} else if len(cp.SchemaFiles) == 0 {
		cp.OmitFullPagesJSON = true
	}

	// Merge deterministic SelectContextFiles hints without inventing new paths.
	for _, f := range candidates {
		classifyHint(f, &cp)
	}

	// Compound: slightly larger budget, still capped.
	if plan.Compound {
		if cp.MaxFiles < 8 {
			cp.MaxFiles = 8
		}
		if cp.History.MaxRecentTurns < 4 && cp.History.Focused {
			cp.History.MaxRecentTurns = 4
		}
	}

	var dupRemoved int
	cp, dupRemoved = Deduplicate(cp)
	if err := Validate(cp); err != nil {
		fb := Fallback("plan_invalid")
		return finalize(fb, start, meta.Candidates, 0)
	}
	return finalize(cp, start, meta.Candidates, dupRemoved)
}

// BuildFromPrompt builds a plan via deterministic BuilderPlan, then ContextPlan.
// On any failure returns Fallback (caller keeps existing context path).
func BuildFromPrompt(prompt string) (ContextPlan, Metrics) {
	plan, err := builderplan.BuildPlan(prompt)
	if err != nil {
		cp := Fallback("build_plan_error")
		return cp, metricsFrom(cp, time.Now(), 0)
	}
	return Build(plan)
}

// Fallback is the safe no-op plan — themebuild must keep existing selection.
func Fallback(reason string) ContextPlan {
	return ContextPlan{
		Valid:           false,
		Source:          "fallback",
		FallbackReason:  reason,
		MaxFiles:        DefaultMaxFiles,
		MaxReference:    DefaultMaxReference,
		MaxBytes:        DefaultMaxBytes,
		IncludeFileTree: true,
		History: HistoryPolicy{
			Focused:        false,
			MaxRecentTurns: summarizeCompatRecent,
			SkipSummarize:  false,
		},
	}
}

const summarizeCompatRecent = 20 // matches themebuild.summarizeHistoryThreshold

func clarifyPlan() ContextPlan {
	return ContextPlan{
		Valid:                true,
		Source:               "builderplan",
		MaxFiles:             0,
		MaxReference:         0,
		MaxBytes:             4_000,
		OmitManifest:         true,
		OmitFullPagesJSON:    true,
		OmitFullDefaultsJSON: true,
		IncludeFileTree:      false,
		History: HistoryPolicy{
			Focused:        true,
			MaxRecentTurns: MinHistoryFocused,
			SkipSummarize:  true,
		},
	}
}

func primaryForSimple(target string) []string {
	if target == "button/style" {
		return []string{"components/button.liquid", "css/theme.css"}
	}
	return []string{"css/theme.css"}
}

func dependencyForSection(target string) []string {
	switch target {
	case "components/header.liquid":
		return []string{"pages/css/header.css", "defaults.json"}
	case "components/footer.liquid":
		return []string{"pages/css/footer.css"}
	default:
		return nil
	}
}

func dependencyForPage(target string) []string {
	low := strings.ToLower(target)
	if strings.Contains(low, "blog") {
		return []string{"pages/css/blog.css"}
	}
	if strings.Contains(low, "home") {
		return []string{"pages/css/home.css"}
	}
	return nil
}

func classifyHint(path string, cp *ContextPlan) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	switch path {
	case "pages.json", "defaults.json":
		cp.SchemaFiles = appendUnique(cp.SchemaFiles, path)
	default:
		if strings.HasPrefix(path, "pages/css/") || strings.HasSuffix(path, ".css") {
			cp.DependencyFiles = appendUnique(cp.DependencyFiles, path)
		} else {
			cp.PrimaryFiles = appendUnique(cp.PrimaryFiles, path)
		}
	}
}

func hasConstraintPrefix(constraints []string, prefix string) bool {
	prefix = strings.ToLower(prefix)
	for _, c := range constraints {
		if strings.HasPrefix(strings.ToLower(c), prefix) {
			return true
		}
	}
	return false
}

func appendUnique(dst []string, paths ...string) []string {
	seen := map[string]bool{}
	for _, p := range dst {
		seen[p] = true
	}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		// Reject absolute / traversal — never accept model-like arbitrary paths.
		if strings.Contains(p, "..") || strings.HasPrefix(p, "/") {
			continue
		}
		seen[p] = true
		dst = append(dst, p)
	}
	return dst
}

func finalize(cp ContextPlan, start time.Time, candidates, dupRemoved int) (ContextPlan, Metrics) {
	meta := metricsFrom(cp, start, candidates)
	meta.DuplicatesRemoved = dupRemoved
	return cp, meta
}

func metricsFrom(cp ContextPlan, start time.Time, candidates int) Metrics {
	all := cp.AllFiles()
	return Metrics{
		ElapsedMs:       time.Since(start).Milliseconds(),
		Candidates:      candidates,
		Selected:        len(all),
		PrimaryCount:    len(cp.PrimaryFiles),
		DependencyCount: len(cp.DependencyFiles),
		SchemaCount:     len(cp.SchemaFiles),
		ReferenceCount:  len(cp.ReferenceFiles),
		HistoryMessages: cp.History.MaxRecentTurns,
		BytesBefore:     EstimateBytes(Fallback("")),
		BytesAfter:      EstimateBytes(cp),
		UsedFallback:    !cp.Valid,
		OmitManifest:    cp.OmitManifest,
		FocusedHistory:  cp.History.Focused,
		ThemeReadsSaved: boolToInt(cp.OmitManifest),
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

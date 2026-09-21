package themebuild

import (
	"log/slog"
	"strings"

	"ai-chat/internal/ai"
	"ai-chat/internal/buildercontext"
	"ai-chat/internal/builderplan"
	"ai-chat/internal/themefs"
)

// buildContextPlan derives a ContextPlan for this generation. On failure it
// returns Fallback so existing context selection remains authoritative.
func buildContextPlan(prompt string, planObs planObservation) (buildercontext.ContextPlan, buildercontext.Metrics) {
	obs := planObs
	if !obs.Valid {
		plan, err := builderplan.BuildPlan(prompt)
		if err != nil {
			return buildercontext.Fallback("build_plan_error"), buildercontext.Metrics{UsedFallback: true}
		}
		obs = planObservation{Plan: plan, Valid: true}
	}
	cp, meta := buildercontext.Build(obs.Plan)
	if !cp.Valid {
		reason := cp.FallbackReason
		if reason == "" {
			reason = "context_plan_invalid"
		}
		return buildercontext.Fallback(reason), meta
	}
	return cp, meta
}

// applyContextPlanToThemeContext shrinks dynamic system grounding. Never
// expands context. When plan is invalid/fallback, tc is unchanged.
func applyContextPlanToThemeContext(tc *ai.ThemeContext, cp buildercontext.ContextPlan, preparedPaths []string) (dupRemoved int) {
	if tc == nil || !cp.Valid {
		return 0
	}
	if cp.OmitManifest {
		tc.Manifest = nil
	}
	if !cp.IncludeFileTree {
		tc.FileTree = nil
	} else if len(preparedPaths) > 0 {
		tc.FileTree = filterFileTreeToPaths(tc.FileTree, preparedPaths)
	} else if files := cp.AllFiles(); len(files) > 0 {
		tc.FileTree = filterFileTreeToPaths(tc.FileTree, files)
	}

	preparedSet := map[string]bool{}
	for _, p := range preparedPaths {
		preparedSet[p] = true
	}
	if cp.OmitFullPagesJSON || preparedSet["pages.json"] {
		before := len(tc.PagesJSON)
		tc.PagesJSON = truncateForSimpleEditPrompt(tc.PagesJSON, 400)
		if before > len(tc.PagesJSON) {
			dupRemoved++
		}
	} else {
		tc.PagesJSON = truncateForSimpleEditPrompt(tc.PagesJSON, 2500)
	}
	if cp.OmitFullDefaultsJSON || preparedSet["defaults.json"] {
		before := len(tc.DefaultsJSON)
		tc.DefaultsJSON = truncateForSimpleEditPrompt(tc.DefaultsJSON, 400)
		if before > len(tc.DefaultsJSON) {
			dupRemoved++
		}
	} else {
		tc.DefaultsJSON = truncateForSimpleEditPrompt(tc.DefaultsJSON, 1200)
	}
	return dupRemoved
}

// selectTurnsForContextPlan applies focused history when the plan asks for it.
// ok=false means the caller should keep the existing summarize / recent path.
func selectTurnsForContextPlan(prior []ai.Turn, cp buildercontext.ContextPlan) (turns []ai.Turn, historyMsgs int, ok bool) {
	if !cp.Valid || !cp.History.Focused || !cp.History.SkipSummarize {
		return nil, 0, false
	}
	turns, historyMsgs, _ = buildercontext.SelectHistory(prior, cp.History)
	return turns, historyMsgs, true
}

func preferPathsFromPlan(cp buildercontext.ContextPlan, planObs planObservation) []string {
	if cp.Valid {
		if files := cp.AllFiles(); len(files) > 0 {
			return files
		}
	}
	if planObs.Valid && len(planObs.CandidateContext) > 0 {
		return planObs.CandidateContext
	}
	return nil
}

// filterPreparedPackageToPaths keeps the preamble plus ### path sections that
// remain in keep. Drops file bodies for paths that were narrowed away so the
// user prompt does not re-inject removed theme content.
func filterPreparedPackageToPaths(pkg string, keep []string) string {
	if strings.TrimSpace(pkg) == "" || len(keep) == 0 {
		return pkg
	}
	keepSet := map[string]bool{}
	for _, p := range keep {
		keepSet[p] = true
	}
	lines := strings.Split(pkg, "\n")
	var out strings.Builder
	include := true // preamble before first ###
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, "### ") {
			path := strings.TrimSpace(strings.TrimPrefix(line, "### "))
			include = keepSet[path]
		}
		if include {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	return strings.TrimRight(out.String(), "\n") + "\n"
}

func estimateThemeContextBytes(tc ai.ThemeContext) int {
	n := len(tc.PagesJSON) + len(tc.DefaultsJSON)
	if tc.Manifest != nil {
		n += 64 * len(tc.Manifest.Components)
	}
	n += estimateFileTreeBytes(tc.FileTree)
	return n
}

func estimateFileTreeBytes(entries []themefs.FileTreeEntry) int {
	n := 0
	var walk func([]themefs.FileTreeEntry)
	walk = func(list []themefs.FileTreeEntry) {
		for _, e := range list {
			n += len(e.Path) + len(e.Name) + 4
			if len(e.Children) > 0 {
				walk(e.Children)
			}
		}
	}
	walk(entries)
	return n
}

func logContextPlan(genID string, tenantID uint64, chatID string, cp buildercontext.ContextPlan, meta buildercontext.Metrics, historyMsgs, bytesBefore, bytesAfter, dupRemoved, themeReadsSaved int) {
	slog.Info("ai: context plan",
		"generation_id", genID,
		"tenant_id", tenantID,
		"chat_id", chatID,
		"context_plan_valid", cp.Valid,
		"context_plan_source", cp.Source,
		"context_plan_fallback", cp.FallbackReason,
		"context_plan_elapsed_ms", meta.ElapsedMs,
		"context_files_candidates", meta.Candidates,
		"context_files_selected", meta.Selected,
		"context_primary_count", meta.PrimaryCount,
		"context_dependency_count", meta.DependencyCount,
		"context_schema_count", meta.SchemaCount,
		"context_reference_count", meta.ReferenceCount,
		"context_history_messages", historyMsgs,
		"context_bytes_before", bytesBefore,
		"context_bytes_after", bytesAfter,
		"context_duplicates_removed", dupRemoved+meta.DuplicatesRemoved,
		"context_theme_reads_saved", themeReadsSaved,
		"context_omit_manifest", cp.OmitManifest,
		"context_focused_history", cp.History.Focused,
		"context_selected_files", cp.AllFiles(),
	)
}

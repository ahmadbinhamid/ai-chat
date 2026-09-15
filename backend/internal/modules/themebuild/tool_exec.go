package themebuild

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"strings"

	"ai-chat/internal/ai"
	"ai-chat/internal/themecheck"
	"ai-chat/internal/themefs"
)

// maxToolReadPaths/maxToolReadBytes bound read_theme_file's own single-call
// footprint — independent of maxEditingFiles-style history bounds (there
// are none anymore, see doc comment on the deleted buildEditingFilesContext):
// the model now asks for exactly the files it wants, so the only thing to
// bound is one call's own size.
const (
	maxToolReadPaths    = 10
	maxToolReadBytes    = 40_000
	maxGrepMatches      = 200
	maxGrepFilesScanned = 500
)

// grepThemeSearchableExt is the set of file types grep_theme will search —
// theme-relative text files only; images/fonts etc. are never candidates.
var grepThemeSearchableExt = map[string]bool{".liquid": true, ".css": true, ".js": true, ".json": true}

// buildToolExecutor returns the ai.ToolExecutor this generation call uses
// to read the real theme — the only place ai.Generate ever reaches
// themefs, and only through this closure (see ai.ToolExecutor's doc
// comment): package ai never imports themefs's Store directly. tc and
// snapBase back validate_changes: tc.GenerationMode lets it apply the same
// mode restriction propose_changes' result would eventually be checked
// against (see execValidateChanges), and snapBase is the per-turn-invariant
// part of a themecheck.Snapshot (see buildSnapshotBase) — built once by
// this function's caller and reused by every validate_changes call this
// executor instance ever handles. validateCallCount is closed over here,
// not passed in, so it persists for the life of this one ToolExecutor
// closure — which doGenerate builds once and checkAndRepair's repair
// rounds reuse unchanged (see their shared toolExec parameter), so the cap
// below spans the whole merchant turn, not just one Generate call.
func (s *Service) buildToolExecutor(store themefs.ThemeStore, storeAuth themefs.RequestAuth, tc ai.ThemeContext, snapBase themecheck.Snapshot) ai.ToolExecutor {
	validateCallCount := 0
	return func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		switch name {
		case "list_theme_files":
			return s.execListThemeFiles(ctx, store, storeAuth)
		case "read_theme_file":
			return s.execReadThemeFile(ctx, store, storeAuth, input)
		case "grep_theme":
			return s.execGrepTheme(ctx, store, storeAuth, input)
		case "validate_changes":
			return s.execValidateChanges(ctx, store, storeAuth, tc, snapBase, &validateCallCount, input)
		default:
			return "", fmt.Errorf("unknown tool %q", name)
		}
	}
}

// buildFileReader returns the ai.FileReader a generation call uses to
// materialize an "edit" action's find/replace pairs into full content (see
// ai.Generate's propose_changes handling) — store here is the same overlay
// store buildToolExecutor above reads through, so a materialized edit sees
// earlier turns' staged draft changes too, never stale saved-theme content.
// Deliberately not routed through ToolExecutor: that returns a
// model-facing formatted string (see execReadThemeFile), not the clean raw
// content MaterializeEdits needs to apply a find/replace against.
func (s *Service) buildFileReader(store themefs.ThemeStore, storeAuth themefs.RequestAuth) ai.FileReader {
	return func(ctx context.Context, path string) (string, error) {
		return store.ReadFile(ctx, storeAuth, path)
	}
}

func (s *Service) execListThemeFiles(ctx context.Context, store themefs.ThemeStore, storeAuth themefs.RequestAuth) (string, error) {
	tree, err := store.ListFiles(ctx, storeAuth)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(tree)
	if err != nil {
		return "", fmt.Errorf("encode file tree: %w", err)
	}
	return string(encoded), nil
}

type readThemeFileInput struct {
	Paths []string `json:"paths"`
}

// execReadThemeFile reads up to maxToolReadPaths files, capping the total
// content returned at maxToolReadBytes — a model asking for several large
// files in one call gets a clear truncation marker rather than a silently
// cut-off response it might mistake for the whole file.
func (s *Service) execReadThemeFile(ctx context.Context, store themefs.ThemeStore, storeAuth themefs.RequestAuth, input json.RawMessage) (string, error) {
	var args readThemeFileInput
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid read_theme_file input: %w", err)
	}
	if len(args.Paths) == 0 {
		return "", fmt.Errorf("paths must not be empty")
	}
	if len(args.Paths) > maxToolReadPaths {
		args.Paths = args.Paths[:maxToolReadPaths]
	}

	var b strings.Builder
	total := 0
	for _, p := range args.Paths {
		// pages.json/defaults.json are rejected here for a reason that has
		// nothing to do with whether they're writable (see
		// themefs.ValidateGeneratedFilePath below, which now allows both as
		// real .json files — the AI theme builder can edit every real theme
		// file, including these two): they're already supplied directly in
		// this call's own context (see THEME_ENGINE_SPEC.md §0 — "never
		// call a tool to fetch these"), so a read_theme_file call for
		// either is always a wasted round trip, not a blocked one. Checked
		// explicitly, ahead of and independent from the write-side
		// allowlist, so a future change to what's writable never silently
		// changes what's worth re-reading via this tool.
		if p == pathPagesJSON || p == pathDefaultsJSON {
			fmt.Fprintf(&b, "### %s\nERROR: %s is already in your context — do not read it via this tool.\n\n", p, p)
			continue
		}
		if err := themefs.ValidateGeneratedFilePath(p); err != nil {
			fmt.Fprintf(&b, "### %s\nERROR: %s\n\n", p, err.Error())
			continue
		}
		// store here is the draft overlay (see doGenerate) — reading
		// through it, not s.store directly, is THE fix this whole feature
		// hinges on: without it, a model that just edited pages/home.liquid
		// and then re-reads it (e.g. before a second, related edit) would
		// see the stale pre-edit content and could silently undo its own
		// prior work.
		content, err := store.ReadFile(ctx, storeAuth, p)
		if err != nil {
			fmt.Fprintf(&b, "### %s\nERROR: %s\n\n", p, err.Error())
			continue
		}
		if content == "" {
			fmt.Fprintf(&b, "### %s\n(does not exist yet)\n\n", p)
			continue
		}
		if total+len(content) > maxToolReadBytes {
			fmt.Fprintf(&b, "(remaining files omitted — total content capped at %d bytes per call; read fewer files per call)\n", maxToolReadBytes)
			break
		}
		total += len(content)
		fmt.Fprintf(&b, "### %s\n%s\n\n", p, content)
	}
	return b.String(), nil
}

type grepThemeInput struct {
	Pattern  string `json:"pattern"`
	PathGlob string `json:"path_glob"`
}

// execGrepTheme searches every searchable theme file for a regular
// expression (RE2 — Go's regexp package, not a plain substring; see
// grepThemeTool's description) matched line-by-line, optionally restricted
// to paths matching path_glob (path.Match — one wildcard segment, no "**").
func (s *Service) execGrepTheme(ctx context.Context, store themefs.ThemeStore, storeAuth themefs.RequestAuth, input json.RawMessage) (string, error) {
	var args grepThemeInput
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid grep_theme input: %w", err)
	}
	if args.Pattern == "" {
		return "", fmt.Errorf("pattern must not be empty")
	}
	re, err := regexp.Compile(args.Pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern: %w", err)
	}

	tree, err := store.ListFiles(ctx, storeAuth)
	if err != nil {
		return "", err
	}
	paths := make(map[string]bool)
	flattenFileTree(tree, paths)

	candidates := make([]string, 0, len(paths))
	for p := range paths {
		if !grepThemeSearchableExt[path.Ext(p)] {
			continue
		}
		if args.PathGlob != "" {
			matched, globErr := path.Match(args.PathGlob, p)
			if globErr != nil {
				return "", fmt.Errorf("invalid path_glob: %w", globErr)
			}
			if !matched {
				continue
			}
		}
		candidates = append(candidates, p)
	}
	sort.Strings(candidates)
	if len(candidates) > maxGrepFilesScanned {
		candidates = candidates[:maxGrepFilesScanned]
	}

	var b strings.Builder
	matches := 0
	for _, p := range candidates {
		if matches >= maxGrepMatches {
			break
		}
		content, err := store.ReadFile(ctx, storeAuth, p)
		if err != nil || content == "" {
			continue
		}
		for i, line := range strings.Split(content, "\n") {
			if matches >= maxGrepMatches {
				break
			}
			if re.MatchString(line) {
				fmt.Fprintf(&b, "%s:%d: %s\n", p, i+1, strings.TrimSpace(line))
				matches++
			}
		}
	}
	if matches == 0 {
		return "(no matches)", nil
	}
	if matches >= maxGrepMatches {
		fmt.Fprintf(&b, "(stopped at %d matches — narrow your pattern/path_glob)\n", maxGrepMatches)
	}
	return b.String(), nil
}

// maxValidateChangesCalls bounds how many times a model can call
// validate_changes in one merchant turn — a free-to-call validation tool is
// the same shape of risk as the production-observed thrash pattern a plain
// exploration tool can already cause (see thrashOutputTokenThreshold's own
// doc comment: 285s / 24,315 output tokens / 6 grep_theme calls that
// changed nothing), just for a new tool instead of an old one. Scoped to
// the whole turn rather than one Generate call — see buildToolExecutor's
// doc comment for why (avoids rebuilding toolExec, and therefore changing
// checkAndRepair's signature, before every repair round).
const maxValidateChangesCalls = 4

// execValidateChanges lets the model check a candidate proposal — the same
// payload it's about to send propose_changes — against themecheck's rules
// without committing it. Advisory only: this never replaces the real,
// authoritative post-hoc check checkAndRepair runs on the actual
// propose_changes result (see its own doc comment) — a model that never
// calls this tool at all is still caught exactly as before. Deliberately
// does not run checkAndRepair's three auto-fixers: this is the model's own
// in-loop check, not a second auto-fix path.
func (s *Service) execValidateChanges(ctx context.Context, store themefs.ThemeStore, storeAuth themefs.RequestAuth, tc ai.ThemeContext, snapBase themecheck.Snapshot, callCount *int, input json.RawMessage) (string, error) {
	var candidate ai.Result
	if err := json.Unmarshal(input, &candidate); err != nil {
		return "", fmt.Errorf("invalid validate_changes input: %w", err)
	}
	if len(candidate.Files) == 0 {
		return "Nothing to validate — files is empty.", nil
	}

	*callCount++
	if *callCount > maxValidateChangesCalls {
		return fmt.Sprintf(
			"validate_changes has already been called %d times this turn. Fix what you have using the findings "+
				"you've already seen and call propose_changes now.", maxValidateChangesCalls), nil
	}

	// Materialize "edit" actions into real content the exact same way
	// Generate does for a real propose_changes call (see
	// ai.MaterializeEdits' doc comment) — otherwise this would validate the
	// find/replace payload itself, not the file content it would actually
	// produce. A fresh failureCounts map per call is deliberate: the
	// escalating "this will fail the generation" messaging
	// MaterializeEdits produces on repeat failures belongs to the real
	// materialization path inside Generate (see editFailureCounts there),
	// not to this advisory check, which has no generation to fail.
	readFile := s.buildFileReader(store, storeAuth)
	ok, retryMsg := ai.MaterializeEdits(ctx, &candidate, readFile, make(map[string]int))
	if !ok {
		// A materialization failure (bad old_string, missing edit target)
		// is exactly the kind of thing this tool exists to catch cheaply —
		// report it as a finding the model can act on, not a tool error.
		return retryMsg, nil
	}

	// themecheck.Check itself has no GenerationMode awareness (see
	// toolsForMode's doc comment) — validateProposal is what actually
	// enforces mode restrictions, post-hoc, in generateValidProposal/
	// checkAndRepair. Running it here too means a mode-restricted candidate
	// that would later be rejected for that reason is never reported as
	// "valid" by this tool. Brand mode itself never reaches this function at
	// all (validate_changes isn't offered — see toolsForMode), so this
	// matters for GenerationModeCopy/GenerationModePages today, and any
	// future mode this file's switch doesn't already special-case.
	if err := validateProposal(&candidate, tc.GenerationMode); err != nil {
		return fmt.Sprintf("This candidate would be rejected: %s", err), nil
	}

	snap := s.buildSnapshot(ctx, store, storeAuth, snapBase, &candidate)
	findings := themecheck.Check(toProposal(&candidate), snap)
	findings = themecheck.DowngradePreExistingFindings(findings, toProposal(&candidate), snap.Files)
	errorFindings, warningFindings := splitFindings(findings)

	slog.Info("validate_changes called", "call_index", *callCount,
		"error_count", len(errorFindings), "warning_count", len(warningFindings), "rules", findingRules(findings))

	if len(errorFindings) == 0 {
		msg := "No blocking findings — proceed to propose_changes."
		if len(warningFindings) > 0 {
			msg += "\n\nNon-blocking warnings (these won't block propose_changes; fix only if easy):\n" +
				formatFindingsList(warningFindings)
		}
		return msg, nil
	}
	msg := "Blocking findings — fix these before calling propose_changes:\n" + formatFindingsList(errorFindings)
	if len(warningFindings) > 0 {
		msg += "\nNon-blocking warnings (these won't block propose_changes; fix only if easy):\n" +
			formatFindingsList(warningFindings)
	}
	return msg, nil
}

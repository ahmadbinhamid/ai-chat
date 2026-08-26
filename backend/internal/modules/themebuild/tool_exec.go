package themebuild

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"ai-chat/internal/ai"
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
// comment): package ai never imports themefs's Store directly.
func (s *Service) buildToolExecutor(store themefs.ThemeStore, storeAuth themefs.RequestAuth) ai.ToolExecutor {
	return func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		switch name {
		case "list_theme_files":
			return s.execListThemeFiles(ctx, store, storeAuth)
		case "read_theme_file":
			return s.execReadThemeFile(ctx, store, storeAuth, input)
		case "grep_theme":
			return s.execGrepTheme(ctx, store, storeAuth, input)
		default:
			return "", fmt.Errorf("unknown tool %q", name)
		}
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

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

// Bounds read_theme_file's footprint (model specifies exact files needed).
const (
	maxToolReadPaths    = 10
	maxToolReadBytes    = 40_000
	maxGrepMatches      = 200
	maxGrepFilesScanned = 500
)

// Text file types only; images/fonts never candidates.
var grepThemeSearchableExt = map[string]bool{".liquid": true, ".css": true, ".js": true, ".json": true}

// Only place ai.Generate reaches themefs; ai never imports themefs directly.
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

// Reads through overlay store (same as buildToolExecutor) so edits see staged draft changes.
func (s *Service) buildFileReader(store themefs.ThemeStore, storeAuth themefs.RequestAuth) ai.FileReader {
	return func(ctx context.Context, path string) (string, error) {
		return store.ReadFile(ctx, storeAuth, path)
	}
}

// For checkAndRepair repair round only; reads previous's files, falls back to base.
func repairFileReader(base ai.FileReader, previous *ai.Result) ai.FileReader {
	byPath := make(map[string]string, len(previous.Files))
	for _, f := range previous.Files {
		byPath[f.Path] = f.Content
	}
	return func(ctx context.Context, path string) (string, error) {
		if content, ok := byPath[path]; ok {
			return content, nil
		}
		return base(ctx, path)
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

// execReadThemeFile reads up to maxToolReadPaths files, capping total content at
// maxToolReadBytes with an explicit truncation marker, not a silent cut-off.
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
		// pages.json/defaults.json are already supplied in this call's own context, so reading
		// them here is always a wasted round trip, not a blocked one — unrelated to writability.
		if p == pathPagesJSON || p == pathDefaultsJSON {
			fmt.Fprintf(&b, "### %s\nERROR: %s is already in your context — do not read it via this tool.\n\n", p, p)
			continue
		}
		if err := themefs.ValidateGeneratedFilePath(p); err != nil {
			fmt.Fprintf(&b, "### %s\nERROR: %s\n\n", p, err.Error())
			continue
		}
		// Reading through the draft overlay, not s.store directly, is required: otherwise a
		// re-read after an edit in this same turn would see stale pre-edit content.
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

// execGrepTheme searches searchable theme files for an RE2 regex, matched line-by-line,
// optionally restricted to paths matching path_glob (one wildcard segment, no "**").
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

package ai

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"ai-chat/internal/themefs"
)

// LargeThemeLimits: above either count the dynamic prompt summarises pages.json, the file tree and the manifest;
// a limit of 0 or less never triggers.
type LargeThemeLimits struct {
	Pages int
	Files int
}

var DefaultLargeThemeLimits = LargeThemeLimits{Pages: 40, Files: 250}

// IsLargeTheme reports whether a theme's grounding should be summarised; an unreadable pages.json counts as no pages.
func IsLargeTheme(pagesJSON string, tree []themefs.FileTreeEntry, l LargeThemeLimits) bool {
	if l.Pages > 0 && len(parsePageTitles(pagesJSON)) > l.Pages {
		return true
	}
	return l.Files > 0 && len(treeFilePaths(tree)) > l.Files
}

type pageTitle struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

func parsePageTitles(pagesJSON string) []pageTitle {
	var pages []pageTitle
	if json.Unmarshal([]byte(pagesJSON), &pages) != nil {
		return nil
	}
	return pages
}

// formatPagesCompact lists "slug — title" sorted by slug; ok is false when pages.json can't be parsed.
func formatPagesCompact(pagesJSON string) (string, bool) {
	var pages []pageTitle
	if json.Unmarshal([]byte(pagesJSON), &pages) != nil {
		return "", false
	}
	sort.SliceStable(pages, func(i, j int) bool { return pages[i].Slug < pages[j].Slug })
	var b strings.Builder
	for _, p := range pages {
		fmt.Fprintf(&b, "  - %s — %s\n", p.Slug, p.Title)
	}
	if len(pages) == 0 {
		b.WriteString("  (none)\n")
	}
	return strings.TrimRight(b.String(), "\n"), true
}

// Directories whose files are listed in full even in a summarised tree: what a turn most often renders or styles.
var fullyListedDirs = []string{"components/", "liquid/partials/", "css/", "js/"}

func treeFilePaths(entries []themefs.FileTreeEntry) []string {
	var out []string
	var walk func([]themefs.FileTreeEntry, string)
	walk = func(es []themefs.FileTreeEntry, parent string) {
		for _, e := range es {
			p := e.Path
			if p == "" {
				p = path.Join(parent, e.Name)
			}
			if e.Type == "directory" || len(e.Children) > 0 {
				walk(e.Children, p)
				continue
			}
			out = append(out, p)
		}
	}
	walk(entries, "")
	return out
}

// formatFileTreeCompact lists every directory with its file count, and files only where fullyListedDirs or keep say
// to; sorted, so the same theme and keep set always give the same text.
func formatFileTreeCompact(entries []themefs.FileTreeEntry, keep map[string]bool) string {
	byDir := map[string][]string{}
	for _, p := range treeFilePaths(entries) {
		dir := path.Dir(p)
		byDir[dir] = append(byDir[dir], p)
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	var b strings.Builder
	b.WriteString("  (summarised for a large theme: directories with file counts, full listings only where shown — list_theme_files returns the full tree)\n")
	for _, d := range dirs {
		files := byDir[d]
		sort.Strings(files)
		label := d + "/"
		if d == "." {
			label = "(theme root)"
		}
		fmt.Fprintf(&b, "  %s (%d files)\n", label, len(files))
		full := fullyListed(d + "/")
		for _, f := range files {
			if full || keep[f] {
				fmt.Fprintf(&b, "    %s\n", path.Base(f))
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func fullyListed(dir string) bool {
	for _, prefix := range fullyListedDirs {
		if strings.HasPrefix(dir, prefix) {
			return true
		}
	}
	return false
}

// formatManifestCompact keeps only components the turn's preloaded or draft files render, and counts the rest.
func formatManifestCompact(m *themefs.Manifest, rendered map[string]bool) string {
	if m == nil || len(m.Components) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("- Components/partials rendered by this turn's loaded and draft files, with their inferred params (pass these explicitly when you render one; read_theme_file it first if a param's purpose isn't obvious from its name):\n")
	rest := 0
	for _, c := range m.Components {
		if !rendered[c.Path] {
			rest++
			continue
		}
		fmt.Fprintf(&b, "  - %s(%s)\n", c.Path, strings.Join(c.Params, ", "))
	}
	if rest > 0 {
		fmt.Fprintf(&b, "  - %d more components; grep or list to find them\n", rest)
	}
	return b.String()
}

func pathSet(lists ...[]string) map[string]bool {
	set := map[string]bool{}
	for _, l := range lists {
		for _, p := range l {
			set[p] = true
		}
	}
	return set
}

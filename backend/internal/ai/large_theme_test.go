package ai

import (
	"fmt"
	"strings"
	"testing"

	"ai-chat/internal/themefs"
)

func file(p string) themefs.FileTreeEntry {
	return themefs.FileTreeEntry{Name: p[strings.LastIndex(p, "/")+1:], Path: p, Type: "file"}
}

func dir(p string, children ...themefs.FileTreeEntry) themefs.FileTreeEntry {
	return themefs.FileTreeEntry{Name: p[strings.LastIndex(p, "/")+1:], Path: p, Type: "directory", Children: children}
}

func pagesJSONOf(n int) string {
	entries := make([]string, n)
	for i := range entries {
		entries[i] = fmt.Sprintf(`{"slug":"page-%03d","title":"Page %d","path":"/pages","page":"page-%03d","seo_title":"long seo text"}`, i, i, i)
	}
	return "[" + strings.Join(entries, ",") + "]"
}

func treeOf(n int) []themefs.FileTreeEntry {
	files := make([]themefs.FileTreeEntry, n)
	for i := range files {
		files[i] = file(fmt.Sprintf("pages/page-%03d.liquid", i))
	}
	return []themefs.FileTreeEntry{dir("pages", files...)}
}

func TestIsLargeTheme(t *testing.T) {
	limits := LargeThemeLimits{Pages: 40, Files: 250}
	tests := []struct {
		name   string
		pages  string
		tree   []themefs.FileTreeEntry
		limits LargeThemeLimits
		want   bool
	}{
		{"small theme", pagesJSONOf(5), treeOf(20), limits, false},
		{"exactly at both limits", pagesJSONOf(40), treeOf(250), limits, false},
		{"over the page limit", pagesJSONOf(41), treeOf(20), limits, true},
		{"over the file limit", pagesJSONOf(5), treeOf(251), limits, true},
		{"malformed pages.json counts as no pages", "not json", treeOf(20), limits, false},
		{"zero limits never trigger", pagesJSONOf(500), treeOf(500), LargeThemeLimits{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsLargeTheme(tt.pages, tt.tree, tt.limits); got != tt.want {
				t.Fatalf("IsLargeTheme = %v, want %v", got, tt.want)
			}
		})
	}
}

func smallThemeContext() ThemeContext {
	return ThemeContext{
		ThemeSlug:    "shop",
		PagesJSON:    `[{"slug":"home","title":"Home","path":"/pages","page":"home"}]`,
		DefaultsJSON: `{"colors":{"primary":"#000"}}`,
		FileTree:     []themefs.FileTreeEntry{dir("pages", file("pages/home.liquid")), dir("components", file("components/header.liquid"))},
		Manifest:     &themefs.Manifest{Components: []themefs.ComponentInfo{{Path: "components/header.liquid", Params: []string{"title"}}}},
		DraftPaths:   []string{"pages/home.liquid"},
	}
}

// Today's exact text for a small theme: any change here breaks every existing chat's prefix cache.
const smallThemeWant = `## Theme being edited
- Theme slug: shop
- MODE: edit
- Current pages.json (existing routes — never register a slug that's already here):
[{"slug":"home","title":"Home","path":"/pages","page":"home"}]
- Current defaults.json (brand colors, fonts, menu, footer — match this, don't invent a different palette):
{"colors":{"primary":"#000"}}
- Current file tree (call list_theme_files again if this feels stale):
pages
  home.liquid
components
  header.liquid
- Existing components/partials and their inferred params (pass these explicitly when you render one; read_theme_file it first if a param's purpose isn't obvious from its name):
  - components/header.liquid(title)
- Files with unsaved changes from earlier turns (the merchant hasn't applied them yet — keep that work; change only what this request needs):
  ` + draftUndoRule + `
  - pages/home.liquid
`

func TestDynamicSystemPrompt_SmallThemeUnchanged(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ThemeContext)
	}{
		{"plain", func(*ThemeContext) {}},
		{"preload and render lists are ignored unless compact", func(tc *ThemeContext) {
			tc.PreloadedPaths = []string{"pages/home.liquid"}
			tc.RenderedComponents = []string{"components/header.liquid"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := smallThemeContext()
			tt.mutate(&tc)
			if got := dynamicSystemPrompt(tc); got != smallThemeWant {
				t.Fatalf("small-theme prompt changed:\n--- got\n%s\n--- want\n%s", got, smallThemeWant)
			}
		})
	}
}

func largeThemeContext() ThemeContext {
	var pages []themefs.FileTreeEntry
	for i := 0; i < 120; i++ {
		pages = append(pages, file(fmt.Sprintf("pages/page-%03d.liquid", i)))
	}
	var components []themefs.ComponentInfo
	for _, c := range []string{"banner", "card", "footer", "grid", "header", "hero"} {
		components = append(components, themefs.ComponentInfo{Path: "components/" + c + ".liquid", Params: []string{"x"}})
	}
	return ThemeContext{
		ThemeSlug: "shop",
		PagesJSON: `[{"slug":"zebra","title":"Zebra Sale","path":"/pages","page":"zebra"},` +
			`{"slug":"about","title":"About Us","path":"/pages","page":"about","seo_description":"` + strings.Repeat("x", 300) + `"}]`,
		DefaultsJSON: `{}`,
		FileTree: []themefs.FileTreeEntry{
			file("pages.json"),
			dir("pages", append(pages, dir("pages/auth", file("pages/auth/login.liquid")))...),
			dir("components", file("components/header.liquid"), file("components/footer.liquid")),
			dir("liquid", dir("liquid/partials", file("liquid/partials/price.liquid")), dir("liquid/layouts", file("liquid/layouts/main.liquid"))),
			dir("css", file("css/theme.css")),
		},
		Manifest:           &themefs.Manifest{Components: components},
		DraftPaths:         []string{"pages/page-007.liquid"},
		PreloadedPaths:     []string{"pages/page-042.liquid"},
		RenderedComponents: []string{"components/header.liquid", "components/hero.liquid"},
		Compact:            true,
	}
}

func TestDynamicSystemPrompt_LargeThemeCompact(t *testing.T) {
	got := dynamicSystemPrompt(largeThemeContext())
	tests := []struct {
		name    string
		want    []string
		notWant []string
	}{
		{"pages.json becomes sorted slug — title lines",
			[]string{"read_theme_file pages.json for full entries", "  - about — About Us\n  - zebra — Zebra Sale"},
			[]string{"seo_description", `"slug"`}},
		{"tree counts every directory",
			[]string{"list_theme_files returns the full tree", "  pages/ (120 files)", "  pages/auth/ (1 files)", "  (theme root) (1 files)", "  liquid/layouts/ (1 files)"},
			nil},
		{"tree lists components, partials and css in full",
			[]string{"  components/ (2 files)\n    footer.liquid\n    header.liquid", "  liquid/partials/ (1 files)\n    price.liquid", "  css/ (1 files)\n    theme.css"},
			nil},
		{"other directories list only preloaded and draft files",
			[]string{"  pages/ (120 files)\n    page-007.liquid\n    page-042.liquid\n"},
			[]string{"page-008.liquid", "main.liquid", "login.liquid"}},
		{"manifest keeps only rendered components and counts the rest",
			[]string{"  - components/header.liquid(x)\n  - components/hero.liquid(x)\n  - 4 more components; grep or list to find them"},
			[]string{"components/card.liquid(", "components/banner.liquid("}},
		{"drafts are still listed", []string{"  - pages/page-007.liquid"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("compact prompt missing %q\n%s", w, got)
				}
			}
			for _, nw := range tt.notWant {
				if strings.Contains(got, nw) {
					t.Errorf("compact prompt should not contain %q", nw)
				}
			}
		})
	}
}

func TestDynamicSystemPrompt_LargeThemeIsDeterministic(t *testing.T) {
	a := largeThemeContext()
	b := largeThemeContext()
	for i, j := 0, len(b.FileTree)-1; i < j; i, j = i+1, j-1 {
		b.FileTree[i], b.FileTree[j] = b.FileTree[j], b.FileTree[i]
	}
	b.DraftPaths = []string{"pages/page-007.liquid"}
	if dynamicSystemPrompt(a) != dynamicSystemPrompt(b) {
		t.Fatal("the same theme in a different tree order gave a different prompt")
	}
}

func TestDynamicSystemPrompt_LargeThemeShrinks(t *testing.T) {
	full := largeThemeContext()
	full.Compact = false
	if compact, whole := len(dynamicSystemPrompt(largeThemeContext())), len(dynamicSystemPrompt(full)); compact >= whole/2 {
		t.Fatalf("compact prompt is %d bytes vs %d in full; expected well under half", compact, whole)
	}
}

func TestDynamicSystemPrompt_CompactFallsBackOnMalformedPages(t *testing.T) {
	tc := smallThemeContext()
	tc.PagesJSON = "not json"
	tc.Compact = true
	full := tc
	full.Compact = false
	if dynamicSystemPrompt(tc) != dynamicSystemPrompt(full) {
		t.Fatal("an unreadable pages.json should keep the full prompt rather than drop the routes")
	}
}

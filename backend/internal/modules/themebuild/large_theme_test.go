package themebuild

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"ai-chat/internal/ai"
	"ai-chat/internal/prefetch"
	"ai-chat/internal/themefs"
)

func grepOver(t *testing.T, files map[string]string, pattern, glob string) string {
	t.Helper()
	ts := newFakeThemeServer(t, files)
	t.Cleanup(ts.Close)
	svc := &Service{store: themefs.NewStore(ts.URL)}
	input, _ := json.Marshal(grepThemeInput{Pattern: pattern, PathGlob: glob})
	out, err := svc.execGrepTheme(context.Background(), themefs.NewCachingStore(svc.store), testStoreAuth(), input)
	if err != nil {
		t.Fatalf("grep_theme failed: %v", err)
	}
	return out
}

func filesWithMatches(n int) map[string]string {
	files := map[string]string{}
	for i := 0; i < n; i++ {
		files[fmt.Sprintf("components/c%03d.liquid", i)] = "<div class=\"hit\"></div>"
	}
	return files
}

func matchLines(out string) int {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if line != "" && !strings.HasPrefix(line, "(") {
			n++
		}
	}
	return n
}

func TestExecGrepTheme_Truncation(t *testing.T) {
	tests := []struct {
		name      string
		files     map[string]string
		wantLines int
		wantNotes []string
		noNotes   []string
	}{
		{"under the cap: every match, no note", filesWithMatches(12), 12, nil, []string{"more matches", "not searched"}},
		{"exactly the cap: no note", filesWithMatches(maxGrepMatches), maxGrepMatches, nil, []string{"more matches"}},
		{"over the cap: says how many more", filesWithMatches(maxGrepMatches + 7), maxGrepMatches,
			[]string{"(7 more matches — narrow pattern or use path_glob)"}, nil},
		{"more files than the scan limit: says which weren't searched", filesWithMatches(maxGrepFilesScanned + 3), maxGrepMatches,
			[]string{fmt.Sprintf("(%d more matches", maxGrepFilesScanned-maxGrepMatches),
				fmt.Sprintf("searched only the first %d of %d matching files", maxGrepFilesScanned, maxGrepFilesScanned+3),
				"3 were not searched"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := grepOver(t, tt.files, "hit", "")
			if got := matchLines(out); got != tt.wantLines {
				t.Fatalf("match lines = %d, want %d:\n%s", got, tt.wantLines, out)
			}
			for _, n := range tt.wantNotes {
				if !strings.Contains(out, n) {
					t.Errorf("missing note %q:\n%s", n, out)
				}
			}
			for _, n := range tt.noNotes {
				if strings.Contains(out, n) {
					t.Errorf("unexpected note %q:\n%s", n, out)
				}
			}
		})
	}
}

func TestExecGrepTheme_NoMatchesStillReportsUnscannedFiles(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < maxGrepFilesScanned+2; i++ {
		files[fmt.Sprintf("components/c%03d.liquid", i)] = "<div></div>"
	}
	out := grepOver(t, files, "nothing-here", "")
	if !strings.Contains(out, "(no matches)") || !strings.Contains(out, "2 were not searched") {
		t.Fatalf("a miss over a truncated scan must say the rest went unsearched:\n%s", out)
	}
	if got := grepOver(t, filesWithMatches(3), "nothing-here", ""); got != "(no matches)" {
		t.Fatalf("a plain miss should stay %q, got %q", "(no matches)", got)
	}
}

func TestExecReadThemeFile_PagesJSONOnlyReadableWhenSummarised(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{"pages.json": `[{"slug":"home"}]`, "defaults.json": `{}`})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}
	tests := []struct {
		name     string
		path     string
		readable bool
		wantFull bool
	}{
		{"full pages.json is in context: refused", "pages.json", false, false},
		{"summarised pages.json: served in full", "pages.json", true, true},
		{"defaults.json is always in context", "defaults.json", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, _ := json.Marshal(readThemeFileInput{Paths: []string{tt.path}})
			out, err := svc.execReadThemeFile(context.Background(), svc.store, testStoreAuth(), input, toolOptions{pagesJSONReadable: tt.readable})
			if err != nil {
				t.Fatalf("read_theme_file failed: %v", err)
			}
			if gotFull := !strings.Contains(out, "already in your context"); gotFull != tt.wantFull {
				t.Fatalf("served in full = %v, want %v:\n%s", gotFull, tt.wantFull, out)
			}
		})
	}
}

func TestSummariseLargeTheme(t *testing.T) {
	var pages []string
	for i := 0; i < 45; i++ {
		pages = append(pages, fmt.Sprintf(`{"slug":"p%d","title":"P%d"}`, i, i))
	}
	large := "[" + strings.Join(pages, ",") + "]"
	preload := prefetch.Result{Files: []prefetch.File{
		{Path: "pages/shop.liquid", Content: "{% render 'components/grid' %}{% render 'components/header' %}"},
		{Path: "components/footer.liquid", Content: "{% render 'components/social' %}"},
	}}
	draft := map[string]string{"pages/about.liquid": "{% include 'components/header' %}{% render 'liquid/partials/price' %}"}

	tests := []struct {
		name         string
		pagesJSON    string
		limits       ai.LargeThemeLimits
		wantCompact  bool
		wantRendered []string
	}{
		{"small theme keeps the full prompt", `[{"slug":"home"}]`, ai.LargeThemeLimits{}, false, nil},
		{"large theme is summarised with what this turn renders", large, ai.LargeThemeLimits{}, true,
			[]string{"components/grid.liquid", "components/header.liquid", "components/social.liquid", "liquid/partials/price.liquid"}},
		{"configured limits are honoured", large, ai.LargeThemeLimits{Pages: 100, Files: 1000}, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &Service{}
			if tt.limits != (ai.LargeThemeLimits{}) {
				svc.SetLargeThemeLimits(tt.limits)
			}
			tc := ai.ThemeContext{PagesJSON: tt.pagesJSON}
			svc.summariseLargeTheme(&tc, preload, draft)
			if tc.Compact != tt.wantCompact {
				t.Fatalf("Compact = %v, want %v", tc.Compact, tt.wantCompact)
			}
			if !reflect.DeepEqual(tc.RenderedComponents, tt.wantRendered) {
				t.Errorf("RenderedComponents = %v, want %v", tc.RenderedComponents, tt.wantRendered)
			}
			if tt.wantCompact && !reflect.DeepEqual(tc.PreloadedPaths, []string{"components/footer.liquid", "pages/shop.liquid"}) {
				t.Errorf("PreloadedPaths = %v, want sorted preloaded paths", tc.PreloadedPaths)
			}
		})
	}
}

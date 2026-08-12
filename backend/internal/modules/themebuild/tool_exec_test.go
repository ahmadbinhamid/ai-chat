package themebuild

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-chat/internal/themefs"
)

// newFakeThemeServer stands in for flowpos-backend's theme-file API,
// serving ListFiles (a flat tree — each map key becomes one top-level
// "file" entry; the tool-exec methods under test only care whether an
// entry is a file, never its nesting) and ReadFile from a fixed in-memory
// file set.
func newFakeThemeServer(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/store/themes/active/files" {
			entries := make([]themefs.FileTreeEntry, 0, len(files))
			for p := range files {
				entries = append(entries, themefs.FileTreeEntry{Name: p, Path: p, Type: "file"})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"files": entries}, "status": true})
			return
		}
		// GET .../files/{path} — themefs.Store percent-encodes each segment.
		reqPath := strings.TrimPrefix(r.URL.Path, "/store/themes/active/files/")
		content, ok := files[reqPath]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"path": reqPath, "content": content, "encoding": "utf-8"},
		})
	}))
}

func testStoreAuth() themefs.RequestAuth { return themefs.RequestAuth{Token: "t", TenantID: 1} }

func TestExecListThemeFiles(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{"pages/home.liquid": "hi", "pages.json": "[]"})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}

	out, err := svc.execListThemeFiles(context.Background(), svc.store, testStoreAuth())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var entries []themefs.FileTreeEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("output isn't valid JSON: %v (%s)", err, out)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d: %+v", len(entries), entries)
	}
}

func TestExecReadThemeFile_Basic(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{
		"components/testimonials.liquid": "<div>hi</div>",
		"pages/offers.liquid":            "offers page",
	})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}

	input, _ := json.Marshal(readThemeFileInput{Paths: []string{"components/testimonials.liquid", "pages/offers.liquid"}})
	out, err := svc.execReadThemeFile(context.Background(), svc.store, testStoreAuth(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "<div>hi</div>") || !strings.Contains(out, "offers page") {
		t.Errorf("expected both files' content in output, got: %s", out)
	}
}

func TestExecReadThemeFile_MissingFile(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}

	input, _ := json.Marshal(readThemeFileInput{Paths: []string{"pages/nope.liquid"}})
	out, err := svc.execReadThemeFile(context.Background(), svc.store, testStoreAuth(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "does not exist yet") {
		t.Errorf("expected a not-found marker, got: %s", out)
	}
}

func TestExecReadThemeFile_RejectsDisallowedExtension(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{"pages.json": "[]"})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}

	// pages.json is a real, readable theme file, but read_theme_file only
	// accepts the extensions ValidateGeneratedFilePath allows (.liquid/.css/
	// .js) — pages.json/defaults.json go through buildThemeContext instead.
	input, _ := json.Marshal(readThemeFileInput{Paths: []string{"pages.json"}})
	out, err := svc.execReadThemeFile(context.Background(), svc.store, testStoreAuth(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "ERROR") {
		t.Errorf("expected a rejection marker for pages.json, got: %s", out)
	}
}

func TestExecReadThemeFile_CapsPathCount(t *testing.T) {
	files := map[string]string{}
	paths := make([]string, 0, 15)
	for i := 0; i < 15; i++ {
		p := fmt.Sprintf("pages/p%d.liquid", i)
		files[p] = fmt.Sprintf("content-%d", i)
		paths = append(paths, p)
	}
	ts := newFakeThemeServer(t, files)
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}

	input, _ := json.Marshal(readThemeFileInput{Paths: paths})
	out, err := svc.execReadThemeFile(context.Background(), svc.store, testStoreAuth(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only the first maxToolReadPaths should ever be attempted.
	if strings.Contains(out, "content-10") {
		t.Errorf("expected paths beyond maxToolReadPaths to be dropped, got: %s", out)
	}
	if !strings.Contains(out, "content-0") {
		t.Errorf("expected the first path's content present, got: %s", out)
	}
}

func TestExecGrepTheme_FindsMatchesWithLineNumbers(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{
		"pages/home.liquid":              "line one\n{% render 'components/testimonials', theme: theme %}\nline three",
		"components/testimonials.liquid": "<div>no match here</div>",
	})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}

	input, _ := json.Marshal(grepThemeInput{Pattern: `render 'components/testimonials'`})
	out, err := svc.execGrepTheme(context.Background(), svc.store, testStoreAuth(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "pages/home.liquid:2:") {
		t.Errorf("expected a match at pages/home.liquid line 2, got: %s", out)
	}
	if strings.Contains(out, "components/testimonials.liquid:") {
		t.Errorf("expected no match in components/testimonials.liquid, got: %s", out)
	}
}

func TestExecGrepTheme_PathGlobRestriction(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{
		"pages/home.liquid":      "TODO marker",
		"components/hero.liquid": "TODO marker",
	})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}

	input, _ := json.Marshal(grepThemeInput{Pattern: "TODO", PathGlob: "pages/*.liquid"})
	out, err := svc.execGrepTheme(context.Background(), svc.store, testStoreAuth(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "pages/home.liquid:") {
		t.Errorf("expected a match in pages/home.liquid, got: %s", out)
	}
	if strings.Contains(out, "components/hero.liquid:") {
		t.Errorf("expected path_glob to exclude components/hero.liquid, got: %s", out)
	}
}

func TestExecGrepTheme_NoMatches(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{"pages/home.liquid": "nothing interesting"})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}

	input, _ := json.Marshal(grepThemeInput{Pattern: "will-not-match-anything"})
	out, err := svc.execGrepTheme(context.Background(), svc.store, testStoreAuth(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "(no matches)" {
		t.Errorf("expected the no-matches marker, got: %q", out)
	}
}

func TestExecGrepTheme_InvalidPattern(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}

	input, _ := json.Marshal(grepThemeInput{Pattern: "(unclosed"})
	if _, err := svc.execGrepTheme(context.Background(), svc.store, testStoreAuth(), input); err == nil {
		t.Error("expected an error for an invalid regex pattern")
	}
}

func TestBuildToolExecutor_UnknownTool(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}

	toolExec := svc.buildToolExecutor(svc.store, testStoreAuth())
	if _, err := toolExec(context.Background(), "not_a_real_tool", nil); err == nil {
		t.Error("expected an error for an unknown tool name")
	}
}

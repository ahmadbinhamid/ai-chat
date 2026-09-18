package themebuild

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/themecheck"
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

// TestBuildFileReader_ReadsThroughDraftOverlay confirms the ai.FileReader
// backing edit materialization (see MaterializeEdits in package ai) reads
// through the SAME draft overlay the model's own read_theme_file tool
// reads through — an edit targeting a file an earlier turn already staged
// must see that staged content, never the stale saved-theme version
// underneath it (same property execReadThemeFile's own test/doc comment
// already establishes for the model-facing read tool).
func TestBuildFileReader_ReadsThroughDraftOverlay(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{"components/footer.liquid": "saved theme content"})
	defer ts.Close()

	svc := &Service{store: themefs.NewStore(ts.URL)}
	overlay := themefs.NewOverlayStore(svc.store, map[string]string{"components/footer.liquid": "staged draft content"})
	readFile := svc.buildFileReader(overlay, testStoreAuth())

	got, err := readFile(context.Background(), "components/footer.liquid")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "staged draft content" {
		t.Errorf("expected the draft-staged content, got %q", got)
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

	// pages.json is a real, readable (and now writable) theme file, but
	// read_theme_file still rejects it — not because of any extension
	// restriction (pages.json is a legitimate .json file, same as any
	// other now — see themefs.allowedGeneratedExtensions), but because
	// it's already supplied directly in context (THEME_ENGINE_SPEC.md
	// §0), so fetching it again through this tool is always a wasted
	// round trip. See execReadThemeFile's own explicit pathPagesJSON/
	// pathDefaultsJSON check.
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
	out, err := svc.execGrepTheme(context.Background(), svc.store, testStoreAuth(), input, nil)
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
	out, err := svc.execGrepTheme(context.Background(), svc.store, testStoreAuth(), input, nil)
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

func TestExecGrepTheme_ReadsConcurrently(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 24; i++ {
		files[fmt.Sprintf("pages/p%d.liquid", i)] = fmt.Sprintf("needle-%d", i)
	}
	var inFlight, maxInFlight int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/store/themes/active/files" {
			entries := make([]themefs.FileTreeEntry, 0, len(files))
			for p := range files {
				entries = append(entries, themefs.FileTreeEntry{Name: p, Path: p, Type: "file"})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"files": entries}})
			return
		}
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			old := atomic.LoadInt32(&maxInFlight)
			if cur <= old || atomic.CompareAndSwapInt32(&maxInFlight, old, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		reqPath := strings.TrimPrefix(r.URL.Path, "/store/themes/active/files/")
		content := files[reqPath]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"path": reqPath, "content": content, "encoding": "utf-8"},
		})
	}))
	defer ts.Close()

	svc := &Service{store: themefs.NewStore(ts.URL)}
	input, _ := json.Marshal(grepThemeInput{Pattern: "needle-"})
	out, err := svc.execGrepTheme(context.Background(), svc.store, testStoreAuth(), input, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "pages/p0.liquid:") {
		t.Fatalf("expected deterministic ordered matches, got: %s", out)
	}
	if maxInFlight < 2 {
		t.Fatalf("expected concurrent ReadFile (maxInFlight>=2), got %d", maxInFlight)
	}
	if maxInFlight > int32(loadThemeFilesConcurrency) {
		t.Fatalf("maxInFlight %d exceeded loadThemeFilesConcurrency %d", maxInFlight, loadThemeFilesConcurrency)
	}
}

func TestExecGrepTheme_ShortCircuitsAtMaxMatches(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 80; i++ {
		var b strings.Builder
		for j := 0; j < 50; j++ {
			fmt.Fprintf(&b, "MATCH %d\n", j)
		}
		files[fmt.Sprintf("pages/p%02d.liquid", i)] = b.String()
	}
	var reads atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/store/themes/active/files" {
			entries := make([]themefs.FileTreeEntry, 0, len(files))
			for p := range files {
				entries = append(entries, themefs.FileTreeEntry{Name: p, Path: p, Type: "file"})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"files": entries}})
			return
		}
		reads.Add(1)
		time.Sleep(5 * time.Millisecond)
		reqPath := strings.TrimPrefix(r.URL.Path, "/store/themes/active/files/")
		content := files[reqPath]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"path": reqPath, "content": content, "encoding": "utf-8"},
		})
	}))
	defer ts.Close()

	svc := &Service{store: themefs.NewStore(ts.URL)}
	input, _ := json.Marshal(grepThemeInput{Pattern: "MATCH"})
	out, err := svc.execGrepTheme(context.Background(), svc.store, testStoreAuth(), input, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "stopped at") {
		t.Fatalf("expected max-match stop notice, got: %s", out)
	}
	if n := reads.Load(); n >= 80 {
		t.Fatalf("expected short-circuit to skip most of 80 files, got %d reads", n)
	}
	// Deterministic ordering: first path in sort order is p00.
	if !strings.HasPrefix(strings.TrimSpace(out), "pages/p00.liquid:") {
		t.Fatalf("expected ordered matches starting at p00, got: %s", out[:min(120, len(out))])
	}
}

func TestExecGrepTheme_InvalidPattern(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}

	input, _ := json.Marshal(grepThemeInput{Pattern: "(unclosed"})
	if _, err := svc.execGrepTheme(context.Background(), svc.store, testStoreAuth(), input, nil); err == nil {
		t.Error("expected an error for an invalid regex pattern")
	}
}

func TestBuildToolExecutor_UnknownTool(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}

	toolExec := svc.buildToolExecutor(svc.store, testStoreAuth(), ai.ThemeContext{}, themecheck.Snapshot{})
	if _, err := toolExec(context.Background(), "not_a_real_tool", nil); err == nil {
		t.Error("expected an error for an unknown tool name")
	}
}

// newRequestCountingThemeServer behaves like newFakeThemeServer, but also
// counts how many times the file-listing endpoint is hit — used to confirm
// the snapshot base (see buildSnapshotBase) is built once and reused, not
// re-fetched on every validate_changes call.
func newRequestCountingThemeServer(t *testing.T, files map[string]string, listCalls *atomic.Int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/store/themes/active/files" {
			listCalls.Add(1)
			entries := make([]themefs.FileTreeEntry, 0, len(files))
			for p := range files {
				entries = append(entries, themefs.FileTreeEntry{Name: p, Path: p, Type: "file"})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"files": entries}, "status": true})
			return
		}
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

// hexColorCSSCandidate builds a validate_changes payload proposing a new
// CSS file with a raw hex color in a color property — a deterministic
// theme-token (rule 8) ERROR finding, since it's a brand-new "create" file
// (nothing to grandfather) and no auto-fixer runs inside validate_changes.
func hexColorCSSCandidate(t *testing.T, path string) json.RawMessage {
	t.Helper()
	in, err := json.Marshal(ai.Result{
		Files:            []ai.GeneratedFile{{Path: path, Action: "create", Content: ".a { color: #ff0000; }"}},
		LayoutLinksToAdd: []string{path}, // registered, so only the theme-token finding is in play
	})
	if err != nil {
		t.Fatalf("marshal candidate: %v", err)
	}
	return in
}

func TestExecValidateChanges_ReportsThemeTokenViolation(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}
	ctx := context.Background()

	base, err := svc.buildSnapshotBase(ctx, svc.store, testStoreAuth())
	if err != nil {
		t.Fatalf("buildSnapshotBase failed: %v", err)
	}
	callCount := 0
	out, err := svc.execValidateChanges(ctx, svc.store, testStoreAuth(), ai.ThemeContext{}, base, &callCount,
		hexColorCSSCandidate(t, "components/css/hero.css"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "theme-token") || !strings.Contains(out, "components/css/hero.css") {
		t.Errorf("expected a theme-token finding naming the file, got: %s", out)
	}
	if !strings.Contains(out, "Blocking findings") {
		t.Errorf("expected the error findings to read as blocking, got: %s", out)
	}
}

func TestExecValidateChanges_NoFindingsReturnsProceedMessage(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}
	ctx := context.Background()

	base, err := svc.buildSnapshotBase(ctx, svc.store, testStoreAuth())
	if err != nil {
		t.Fatalf("buildSnapshotBase failed: %v", err)
	}
	in, _ := json.Marshal(ai.Result{
		Files:            []ai.GeneratedFile{{Path: "components/css/hero.css", Action: "create", Content: ".a { color: var(--theme-primary, #ff0000); }"}},
		LayoutLinksToAdd: []string{"components/css/hero.css"},
	})
	callCount := 0
	out, err := svc.execValidateChanges(ctx, svc.store, testStoreAuth(), ai.ThemeContext{}, base, &callCount, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "No blocking findings") {
		t.Errorf("expected the proceed message, got: %s", out)
	}
}

func TestExecValidateChanges_WarningsMarkedNonBlocking(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}
	ctx := context.Background()

	base, err := svc.buildSnapshotBase(ctx, svc.store, testStoreAuth())
	if err != nil {
		t.Fatalf("buildSnapshotBase failed: %v", err)
	}
	// A raw hex value inside a --custom-property declaration is only a
	// warning (rule 8 permits component-local tokens with literal values —
	// see checkThemeToken's own doc comment), so this candidate has no
	// blocking findings but does have this one.
	in, _ := json.Marshal(ai.Result{
		Files:            []ai.GeneratedFile{{Path: "components/css/hero.css", Action: "create", Content: ".a { --hero-accent: #ff0000; }"}},
		LayoutLinksToAdd: []string{"components/css/hero.css"},
	})
	callCount := 0
	out, err := svc.execValidateChanges(ctx, svc.store, testStoreAuth(), ai.ThemeContext{}, base, &callCount, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "No blocking findings") {
		t.Errorf("expected no blocking findings, got: %s", out)
	}
	if !strings.Contains(out, "Non-blocking warnings") || !strings.Contains(out, "theme-token") {
		t.Errorf("expected the warning to be surfaced and marked non-blocking, got: %s", out)
	}
}

func TestExecValidateChanges_CallCapEnforced(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}
	ctx := context.Background()

	base, err := svc.buildSnapshotBase(ctx, svc.store, testStoreAuth())
	if err != nil {
		t.Fatalf("buildSnapshotBase failed: %v", err)
	}
	callCount := 0
	var lastOut string
	for i := 0; i < maxValidateChangesCalls+1; i++ {
		out, err := svc.execValidateChanges(ctx, svc.store, testStoreAuth(), ai.ThemeContext{}, base, &callCount,
			hexColorCSSCandidate(t, "components/css/hero.css"))
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
		lastOut = out
	}
	if !strings.Contains(lastOut, fmt.Sprintf("already been called %d times", maxValidateChangesCalls)) {
		t.Errorf("expected the over-cap message on call %d, got: %s", maxValidateChangesCalls+1, lastOut)
	}
}

func TestExecValidateChanges_EmptyFilesArrayDoesNotCountAgainstCap(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}
	ctx := context.Background()

	base, err := svc.buildSnapshotBase(ctx, svc.store, testStoreAuth())
	if err != nil {
		t.Fatalf("buildSnapshotBase failed: %v", err)
	}
	emptyIn, _ := json.Marshal(ai.Result{Files: []ai.GeneratedFile{}})
	callCount := 0
	for i := 0; i < maxValidateChangesCalls+3; i++ {
		out, err := svc.execValidateChanges(ctx, svc.store, testStoreAuth(), ai.ThemeContext{}, base, &callCount, emptyIn)
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
		if !strings.Contains(out, "Nothing to validate") {
			t.Fatalf("call %d: expected the empty-files message, got: %s", i, out)
		}
	}
	if callCount != 0 {
		t.Errorf("expected empty-files calls to never increment the cap counter, got %d", callCount)
	}

	// A real call right after should still be well within budget.
	out, err := svc.execValidateChanges(ctx, svc.store, testStoreAuth(), ai.ThemeContext{}, base, &callCount,
		hexColorCSSCandidate(t, "components/css/hero.css"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out, "already been called") {
		t.Errorf("expected the empty-files calls not to count against the cap, got: %s", out)
	}
}

func TestExecValidateChanges_EditActionMaterializedBeforeChecking(t *testing.T) {
	const path = "components/css/hero.css"
	ts := newFakeThemeServer(t, map[string]string{
		path: ".a { color: var(--theme-primary, #111111); }",
	})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}
	ctx := context.Background()

	base, err := svc.buildSnapshotBase(ctx, svc.store, testStoreAuth())
	if err != nil {
		t.Fatalf("buildSnapshotBase failed: %v", err)
	}
	// This edit strips the var()/fallback wrapper, leaving a raw hex color —
	// only valid after materialization resolves the edit against the file's
	// real current content; checking the edit payload itself (old_string/
	// new_string) would never see this violation at all.
	in, _ := json.Marshal(ai.Result{
		Files: []ai.GeneratedFile{{
			Path: path, Action: "edit",
			Edits: []ai.Edit{{OldString: "var(--theme-primary, #111111)", NewString: "#ff0000"}},
		}},
	})
	callCount := 0
	out, err := svc.execValidateChanges(ctx, svc.store, testStoreAuth(), ai.ThemeContext{}, base, &callCount, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "theme-token") || !strings.Contains(out, path) {
		t.Errorf("expected the materialized content's theme-token violation to be reported, got: %s", out)
	}
}

// TestBuildToolExecutor_ValidateChangesReusesSnapshotBase confirms the base
// built once by buildSnapshotBase (see the doGenerate call site) is what
// validate_changes reuses across several calls — the file-listing endpoint
// must never be hit again by any of them.
func TestBuildToolExecutor_ValidateChangesReusesSnapshotBase(t *testing.T) {
	var listCalls atomic.Int64
	ts := newRequestCountingThemeServer(t, map[string]string{}, &listCalls)
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}
	ctx := context.Background()

	base, err := svc.buildSnapshotBase(ctx, svc.store, testStoreAuth())
	if err != nil {
		t.Fatalf("buildSnapshotBase failed: %v", err)
	}
	if got := listCalls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 list-files call to build the base, got %d", got)
	}

	toolExec := svc.buildToolExecutor(svc.store, testStoreAuth(), ai.ThemeContext{}, base)
	for i := 0; i < 3; i++ {
		if _, err := toolExec(ctx, "validate_changes", hexColorCSSCandidate(t, fmt.Sprintf("components/css/hero%d.css", i))); err != nil {
			t.Fatalf("validate_changes call %d failed: %v", i, err)
		}
	}
	if got := listCalls.Load(); got != 1 {
		t.Errorf("expected the snapshot base's single list-files call to be reused, got %d total calls", got)
	}
}

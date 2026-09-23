package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// captureWarnLogs redirects slog's default logger to a buffer for fn, restoring the original
// afterward, and returns every non-empty line written.
func captureWarnLogs(t *testing.T, fn func()) []string {
	t.Helper()
	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(original)
	fn()
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func TestWarnReadBeforeWriteViolations_ReadFileProducesNoWarning(t *testing.T) {
	files := []GeneratedFile{{Path: "components/footer.liquid", Action: "update"}}
	known := map[string]bool{"components/footer.liquid": true}
	lines := captureWarnLogs(t, func() { warnReadBeforeWriteViolations(files, known) })
	if len(lines) != 0 {
		t.Errorf("expected no warnings for a file read earlier in the loop, got: %v", lines)
	}
}

func TestWarnReadBeforeWriteViolations_UnreadFileWarnsWithPath(t *testing.T) {
	files := []GeneratedFile{{Path: "components/footer.liquid", Action: "update"}}
	known := map[string]bool{}
	lines := captureWarnLogs(t, func() { warnReadBeforeWriteViolations(files, known) })
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 warning, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "components/footer.liquid") {
		t.Errorf("expected the warning to name the unread path, got: %s", lines[0])
	}
}

func TestWarnReadBeforeWriteViolations_CreateNeverWarns(t *testing.T) {
	files := []GeneratedFile{{Path: "components/new-thing.liquid", Action: "create"}}
	known := map[string]bool{}
	lines := captureWarnLogs(t, func() { warnReadBeforeWriteViolations(files, known) })
	if len(lines) != 0 {
		t.Errorf("expected no warning for a create file, got: %v", lines)
	}
}

// TestWarnReadBeforeWriteViolations_PreSuppliedFilesNeverWarn checks pages.json/defaults.json
func TestWarnReadBeforeWriteViolations_PreSuppliedFilesNeverWarn(t *testing.T) {
	files := []GeneratedFile{
		{Path: "defaults.json", Action: "update"},
		{Path: "pages.json", Action: "update"},
	}
	known := map[string]bool{}
	lines := captureWarnLogs(t, func() { warnReadBeforeWriteViolations(files, known) })
	if len(lines) != 0 {
		t.Errorf("expected no warnings for pages.json/defaults.json written unread, got: %v", lines)
	}
}

// TestWarnReadBeforeWriteViolations_LayoutFilesStillWarn checks the exclusion is narrow:
// layout-start/end.liquid are NOT pre-supplied, so writing one unread still warns.
func TestWarnReadBeforeWriteViolations_LayoutFilesStillWarn(t *testing.T) {
	files := []GeneratedFile{
		{Path: "liquid/layout-start.liquid", Action: "update"},
		{Path: "liquid/layout-end.liquid", Action: "update"},
	}
	known := map[string]bool{}
	lines := captureWarnLogs(t, func() { warnReadBeforeWriteViolations(files, known) })
	if len(lines) != 2 {
		t.Fatalf("expected both layout files to warn when written unread, got %d: %v", len(lines), lines)
	}
	for _, path := range []string{"liquid/layout-start.liquid", "liquid/layout-end.liquid"} {
		found := false
		for _, l := range lines {
			if strings.Contains(l, path) {
				found = true
			}
		}
		if !found {
			t.Errorf("expected a warning naming %s, got: %v", path, lines)
		}
	}
}

// TestWarnReadBeforeWriteViolations_OrdinaryFileStillWarns checks the exclusion is scoped
// to exactly the two pre-supplied names.
func TestWarnReadBeforeWriteViolations_OrdinaryFileStillWarns(t *testing.T) {
	files := []GeneratedFile{{Path: "components/footer.liquid", Action: "update"}}
	known := map[string]bool{}
	lines := captureWarnLogs(t, func() { warnReadBeforeWriteViolations(files, known) })
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 warning for an ordinary unread update, got %d: %v", len(lines), lines)
	}
}

func TestRegisterReadPaths_AllBatchedPathsRegister(t *testing.T) {
	paths := make([]string, 10)
	for i := range paths {
		paths[i] = fmt.Sprintf("pages/p%d.liquid", i)
	}
	input, err := json.Marshal(map[string]any{"paths": paths})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	known := map[string]bool{}
	registerReadPaths(input, known)
	for _, p := range paths {
		if !known[p] {
			t.Errorf("expected path %q to register from the batched read, got known=%v", p, known)
		}
	}
	if len(known) != 10 {
		t.Errorf("expected exactly 10 registered paths, got %d", len(known))
	}
}

// TestGenerate_NoWarningWhenUpdatedFileWasRead is the end-to-end version: through the real
// tool loop, a file read_theme_file actually fetched must not warn.
func TestGenerate_NoWarningWhenUpdatedFileWasRead(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		switch calls {
		case 1:
			fmt.Fprint(w, toolUseSSEResponse("msg_1", "toolu_1", "read_theme_file",
				map[string]any{"paths": []string{"components/footer.liquid"}}, 50, 10))
		case 2:
			fmt.Fprint(w, toolUseSSEResponse("msg_2", "toolu_2", "propose_changes", map[string]any{
				"summary":               "Updated the footer.",
				"needs_clarification":   false,
				"files":                 []map[string]any{{"path": "components/footer.liquid", "action": "update", "content": "NEW"}},
				"page_registry_entry":   nil,
				"layout_links_to_add":   []string{},
				"layout_scripts_to_add": []string{},
			}, 30, 40))
		default:
			t.Errorf("unexpected 3rd call to the fake Anthropic server")
		}
	}))
	defer ts.Close()

	client := anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("test-key"))
	g := newTestGenerator(client)
	toolExec := func(_ context.Context, name string, input json.RawMessage) (string, error) {
		return "### components/footer.liquid\nold content", nil
	}

	var genErr error
	lines := captureWarnLogs(t, func() {
		_, genErr = g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil,
			"update the footer", nil, nil, nil, toolExec, nil)
	})
	if genErr != nil {
		t.Fatalf("Generate returned an error: %v", genErr)
	}
	for _, l := range lines {
		if strings.Contains(l, "never read this generation") {
			t.Errorf("expected no read-before-write warning for a file that was read, got: %v", lines)
		}
	}
}

// TestGenerate_WarnsOnUpdateToFileOnlyGrepped checks a file only matched by grep_theme
// (never read_theme_file) still warns, and the proposal comes back completely unchanged.
func TestGenerate_WarnsOnUpdateToFileOnlyGrepped(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		switch calls {
		case 1:
			fmt.Fprint(w, toolUseSSEResponse("msg_1", "toolu_1", "grep_theme",
				map[string]any{"pattern": "footer"}, 50, 10))
		case 2:
			fmt.Fprint(w, toolUseSSEResponse("msg_2", "toolu_2", "propose_changes", map[string]any{
				"summary":               "Updated the footer.",
				"needs_clarification":   false,
				"files":                 []map[string]any{{"path": "components/footer.liquid", "action": "update", "content": "NEW"}},
				"page_registry_entry":   nil,
				"layout_links_to_add":   []string{},
				"layout_scripts_to_add": []string{},
			}, 30, 40))
		default:
			t.Errorf("unexpected 3rd call to the fake Anthropic server")
		}
	}))
	defer ts.Close()

	client := anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("test-key"))
	g := newTestGenerator(client)
	toolExec := func(_ context.Context, name string, _ json.RawMessage) (string, error) {
		if name != toolNameGrepTheme {
			t.Errorf("unexpected tool call %q", name)
			return "", fmt.Errorf("unexpected tool %q", name)
		}
		return "components/footer.liquid:1: footer", nil
	}

	var result *Result
	var genErr error
	lines := captureWarnLogs(t, func() {
		result, genErr = g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil,
			"update the footer", nil, nil, nil, toolExec, nil)
	})
	if genErr != nil {
		t.Fatalf("Generate returned an error: %v", genErr)
	}
	// The proposal itself is untouched by the warning — same content, same
	// number of API calls (no retry, no rejection triggered by it).
	if len(result.Files) != 1 || result.Files[0].Path != "components/footer.liquid" || result.Files[0].Content != "NEW" {
		t.Fatalf("expected the proposal to come back unchanged, got: %+v", result.Files)
	}
	if calls != 2 {
		t.Fatalf("expected exactly 2 API calls (grep_theme is not a read, but must not trigger a retry either), got %d", calls)
	}
	found := false
	for _, l := range lines {
		if strings.Contains(l, "components/footer.liquid") && strings.Contains(l, "never read this generation") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a warning naming components/footer.liquid (only grepped, never read), got: %v", lines)
	}
}

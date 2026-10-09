package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"
)

// captureWarnings returns the JSON log records written at Warn or above for the rest of the test.
func captureWarnings(t *testing.T) func() []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() []map[string]any {
		var out []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			var rec map[string]any
			if json.Unmarshal([]byte(line), &rec) == nil {
				out = append(out, rec)
			}
		}
		return out
	}
}

func TestMaterializeEdits_LogsTheFailingOldString(t *testing.T) {
	// Byte 300 falls inside the two-byte "é", so the cut must step back to keep valid UTF-8.
	long := strings.Repeat("a", 299) + "é" + strings.Repeat("b", 100)
	tests := []struct {
		name     string
		content  string
		edits    []Edit
		wantOld  string
		wantEdit int
	}{
		{name: "no match names the second edit", content: ".a { color: red; }\n",
			edits:   []Edit{{OldString: ".a { color: red; }", NewString: ".a { color: blue; }"}, {OldString: ".b { color: red; }", NewString: "x"}},
			wantOld: ".b { color: red; }", wantEdit: 2},
		{name: "ambiguous match", content: ".a { color: red; }\n.b { color: red; }\n",
			edits: []Edit{{OldString: "color: red;", NewString: "color: blue;"}}, wantOld: "color: red;", wantEdit: 1},
		{name: "long old_string is cut at 300 bytes on a rune boundary", content: "x\n",
			edits: []Edit{{OldString: long, NewString: "y"}}, wantOld: strings.Repeat("a", 299) + "…", wantEdit: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureWarnings(t)
			result := &Result{Files: []GeneratedFile{{Path: "components/css/footer.css", Action: "edit", Edits: tt.edits}}}
			read := func(context.Context, string) (string, error) { return tt.content, nil }
			if ok, _ := materializeEdits(context.Background(), result, read, map[string]int{}); ok {
				t.Fatal("want the edit to fail")
			}
			var rec map[string]any
			for _, r := range logs() {
				if r["msg"] == "ai: edit materialization failed" {
					rec = r
				}
			}
			if rec == nil {
				t.Fatal("no edit failure logged")
			}
			got, _ := rec["old_string"].(string)
			if got != tt.wantOld || !utf8.ValidString(got) {
				t.Errorf("old_string = %q, want %q", got, tt.wantOld)
			}
			if rec["edit_count"] != float64(len(tt.edits)) || rec["old_bytes"] == nil || rec["new_bytes"] == nil {
				t.Errorf("want edit_count/old_bytes/new_bytes logged, got %v", rec)
			}
		})
	}
}

// Wrapping the error to remember which edit failed must not change its text, which tests on main match on.
func TestEditFailure_KeepsTheErrorText(t *testing.T) {
	_, _, _, err := applyEdits("one\n", []Edit{{OldString: "one", NewString: "1"}, {OldString: "two", NewString: "2"}})
	if err == nil || err.Error() != "edit 2: old_string not found (0 matches)" {
		t.Errorf("err = %v", err)
	}
}

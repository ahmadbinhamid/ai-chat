package themebuild

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ai-chat/internal/ai"
	"ai-chat/internal/themecheck"
	"ai-chat/internal/themefs"
)

// mapThemeStore is a read-only ThemeStore over a map; a missing path reads as an error.
type mapThemeStore struct{ files map[string]string }

func (m mapThemeStore) ReadFile(_ context.Context, _ themefs.RequestAuth, p string) (string, error) {
	c, ok := m.files[p]
	if !ok {
		return "", errors.New("not found")
	}
	return c, nil
}
func (m mapThemeStore) WriteFile(context.Context, themefs.RequestAuth, string, string, *themefs.PageMeta) error {
	return errors.New("read-only")
}
func (m mapThemeStore) DeleteFile(context.Context, themefs.RequestAuth, string) error {
	return errors.New("read-only")
}
func (m mapThemeStore) ListFiles(context.Context, themefs.RequestAuth) ([]themefs.FileTreeEntry, error) {
	return nil, nil
}

func TestDraftReversionWarnings(t *testing.T) {
	path := "components/css/header.css"
	saved := ".header {\n  padding: 16px;\n}\n"
	draft := map[string]string{
		path:             ".header {\n  padding: 16px;\n  background: #111;\n  color: #eee;\n  border-bottom: 1px solid #333;\n}\n",
		"js/new.js":      "(function () { window.New = 1; })();\n",
		"pages/x.liquid": "{{ page.title }}",
	}
	svc := &Service{store: mapThemeStore{files: map[string]string{path: saved}}}
	result := &ai.Result{Files: []ai.GeneratedFile{
		{Path: path, Action: "update", Content: saved},                          // the add-to-cart fix rewrote the file from the saved version
		{Path: "js/new.js", Action: "update", Content: "(function () {})();\n"}, // created in draft, not in saved: skipped
		{Path: "js/minicart.js", Action: "update", Content: "x"},                // not in draft: skipped
	}}
	before := result.Files[0].Content

	got := svc.draftReversionWarnings(context.Background(), themefs.RequestAuth{}, "chat-1", draft, result)

	if len(got) != 1 || got[0].Path != path || got[0].Severity != themecheck.SeverityWarning {
		t.Fatalf("expected one warning for %s, got %+v", path, got)
	}
	if result.Files[0].Content != before {
		t.Error("detection must never modify the proposal")
	}
	note := appendWarningsNote("Fixed the add to cart button.", got)
	if !strings.Contains(note, path+": This change may undo some of your earlier unsaved changes") {
		t.Errorf("expected the merchant-facing note to include the warning, got %q", note)
	}
}

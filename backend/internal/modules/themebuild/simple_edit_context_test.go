package themebuild

import (
	"context"
	"path"
	"strings"
	"testing"

	"ai-chat/internal/themefs"
)

func TestDetectSimpleEditTargets(t *testing.T) {
	got := detectSimpleEditTargets("change the header design")
	if len(got) == 0 || got[0] != "header" {
		t.Fatalf("got %v", got)
	}
	got = detectSimpleEditTargets("make the product cards more modern")
	if len(got) == 0 || got[0] != "product_card" {
		t.Fatalf("got %v", got)
	}
	got = detectSimpleEditTargets("change the homepage hero section")
	if len(got) < 2 {
		t.Fatalf("expected homepage+hero, got %v", got)
	}
	got = detectSimpleEditTargets("change the slider color")
	if len(got) == 0 || got[0] != "slider" {
		t.Fatalf("expected slider target, got %v", got)
	}
}

func TestBuildSimpleEditContext_Header(t *testing.T) {
	var big strings.Builder
	for i := 0; i < 400; i++ {
		big.WriteString("<div class=\"row\">line</div>\n")
	}
	store := &memThemeStore{files: map[string]string{
		"components/header.liquid": "<header>\n" + big.String() + "</header>\n",
		"pages/home.liquid":        "home\n",
		"assets/style.css":         "body{}\n",
	}}
	sec, err := BuildSimpleEditContext(context.Background(), store, themefs.RequestAuth{}, "change the header design")
	if err != nil {
		t.Fatal(err)
	}
	if !sec.Sufficient {
		t.Fatal("expected sufficient context")
	}
	if len(sec.Paths) == 0 || sec.Paths[0] != "components/header.liquid" {
		t.Fatalf("paths=%v", sec.Paths)
	}
	if !strings.Contains(sec.Package, "header.liquid") && !strings.Contains(sec.Package, "header") {
		t.Fatalf("package missing header")
	}
	if strings.Count(sec.Package, "\n") > 400 {
		t.Fatalf("expected bounded package, lines=%d", strings.Count(sec.Package, "\n"))
	}
}

func TestRankPathsForTargets_ProductCard(t *testing.T) {
	paths := []string{
		"components/product-card.liquid",
		"pages/home.liquid",
		"components/header.liquid",
	}
	got := rankPathsForTargets(paths, []string{"product_card"}, "make the product cards more modern")
	if len(got) == 0 || got[0] != "components/product-card.liquid" {
		t.Fatalf("got %v", got)
	}
}

type memThemeStore struct {
	files map[string]string
}

func (m *memThemeStore) ReadFile(_ context.Context, _ themefs.RequestAuth, relPath string) (string, error) {
	return m.files[relPath], nil
}
func (m *memThemeStore) WriteFile(context.Context, themefs.RequestAuth, string, string, *themefs.PageMeta) error {
	return nil
}
func (m *memThemeStore) DeleteFile(context.Context, themefs.RequestAuth, string) error { return nil }
func (m *memThemeStore) ListFiles(context.Context, themefs.RequestAuth) ([]themefs.FileTreeEntry, error) {
	var out []themefs.FileTreeEntry
	for p := range m.files {
		out = append(out, themefs.FileTreeEntry{Name: path.Base(p), Path: p, Type: "file"})
	}
	return out, nil
}

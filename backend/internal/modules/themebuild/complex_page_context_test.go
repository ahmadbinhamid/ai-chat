package themebuild

import (
	"context"
	"strings"
	"testing"

	"ai-chat/internal/themefs"
)

func TestBuildComplexPageContext_RanksPagesAndMenu(t *testing.T) {
	store := &memThemeStore{files: map[string]string{
		"pages.json":               `[{"slug":"home"}]`,
		"pages/home.liquid":        "<h1>Home</h1>",
		"pages/about.liquid":       "<h1>About</h1>",
		"components/header.liquid": `<nav><a href="/">Home</a></nav>`,
		"components/footer.liquid": "footer",
		"assets/unrelated.css":     "body{}",
		"defaults.json":            `{}`,
	}}
	cpc, err := BuildComplexPageContext(context.Background(), store, themefs.RequestAuth{}, "create a Contact Us page and add it to the menu")
	if err != nil {
		t.Fatal(err)
	}
	if len(cpc.Paths) == 0 {
		t.Fatal("expected ranked paths")
	}
	joined := strings.Join(cpc.Paths, "|")
	if !strings.Contains(joined, "pages.json") {
		t.Fatalf("expected pages.json in %v", cpc.Paths)
	}
	if !strings.Contains(joined, "header") && !strings.Contains(joined, "menu") && !strings.Contains(joined, "nav") {
		t.Fatalf("expected menu/header path in %v", cpc.Paths)
	}
	if len(cpc.Package) < 20 {
		t.Fatal("expected non-empty package")
	}
	if intentUsesSimpleEditOneShot(ClassifyIntent("create a Contact Us page and add it to the menu", "", false)) {
		t.Fatal("page+menu must not enter simple_edit one-shot")
	}
}

func TestRankPathsForPageCreate_LimitsSamplePages(t *testing.T) {
	paths := []string{
		"pages.json",
		"pages/home.liquid",
		"pages/about.liquid",
		"pages/shop.liquid",
		"components/header.liquid",
	}
	got := rankPathsForPageCreate(paths, "create FAQ page")
	pageCount := 0
	for _, p := range got {
		if strings.Contains(p, "pages/") && strings.HasSuffix(p, ".liquid") {
			pageCount++
		}
	}
	if pageCount > 1 {
		t.Fatalf("expected at most one sample page, got %d in %v", pageCount, got)
	}
}

func TestComplexPageBudgets_Bounded(t *testing.T) {
	if maxComplexPageModelCalls > 10 {
		t.Fatalf("complex page iteration budget too high: %d", maxComplexPageModelCalls)
	}
	if maxComplexExploration > 12 {
		t.Fatalf("complex exploration budget too high: %d", maxComplexExploration)
	}
	if simpleEditMaxTokens != 8000 {
		t.Fatalf("simple_edit max_tokens must stay 8000, got %d", simpleEditMaxTokens)
	}
}

package themebuild

import (
	"context"
	"strings"
	"testing"
	"time"

	"ai-chat/internal/ai"
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
	if !cpc.Sufficient {
		t.Fatal("page+menu context should be sufficient")
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

func TestRankPathsForPageCreate_HomeRedesignPrefersHomeOverHeader(t *testing.T) {
	paths := []string{
		"pages.json",
		"pages/home.liquid",
		"pages/about.liquid",
		"components/header.liquid",
		"components/header-menu.liquid",
		"components/css/header.css",
		"components/css/header-menu.css",
		"components/css/home.css",
		"components/hero-slider.liquid",
		"components/js/slider.js",
		"sections/home-hero.liquid",
	}
	got := rankPathsForPageCreate(paths, "change home page design with a slider and beautiful home page")
	if len(got) == 0 || got[0] != "pages/home.liquid" {
		t.Fatalf("expected pages/home.liquid first for homepage redesign, got %v", got)
	}
	joined := strings.Join(got, "|")
	if strings.Contains(joined, "header") {
		t.Fatalf("homepage redesign must not prefer header files, got %v", got)
	}
	if !strings.Contains(joined, "home.css") && !strings.Contains(joined, "hero") && !strings.Contains(joined, "slider") {
		t.Fatalf("expected homepage/hero/slider assets in %v", got)
	}
	// pages.json must not crowd out hero/slider when both exist
	if len(got) >= 3 {
		top := strings.Join(got[:3], "|")
		if strings.Contains(top, "pages.json") && !strings.Contains(top, "home") && !strings.Contains(top, "hero") && !strings.Contains(top, "slider") {
			t.Fatalf("top ranks should be homepage assets, got %v", got)
		}
	}
}

func TestRankPathsForPageCreate_SliderMultiImage(t *testing.T) {
	paths := []string{
		"pages.json",
		"pages/home.liquid",
		"components/header.liquid",
		"components/card-essentials.liquid",
		"components/store-hero-banner.liquid",
		"components/css/store-hero-banner.css",
		"js/store-hero-banner.js",
		"js/testimonials.js",
		"liquid/layout-end.liquid",
		"components/css/home.css",
	}
	got := rankPathsForPageCreate(paths, "an we use multilpal imges on slider and auto scrol please")
	if len(got) == 0 {
		t.Fatal("expected ranked paths")
	}
	joined := strings.Join(got, "|")
	if !strings.Contains(joined, "store-hero-banner") {
		t.Fatalf("expected store-hero-banner paths, got %v", got)
	}
	if !strings.Contains(joined, "store-hero-banner.js") {
		t.Fatalf("autoplay slider must include hero JS, got %v", got)
	}
	if !strings.Contains(joined, "layout-end") {
		t.Fatalf("autoplay slider must include layout-end for script tag, got %v", got)
	}
	if strings.Contains(joined, "card-essentials") {
		t.Fatalf("must not prefer unrelated cards over slider, got %v", got)
	}
	if !isHomePageRedesignPrompt("an we use multilpal imges on slider and auto scrol please") {
		t.Fatal("slider multi-image must use homepage ranking")
	}
}

func TestPromptWantsHeaderOrNav_NotInnovative(t *testing.T) {
	if promptWantsHeaderOrNav("make an innovative beautiful homepage with a slider") {
		t.Fatal("innovative must not trip nav detection")
	}
	if !promptWantsHeaderOrNav("create a page and add it to the menu") {
		t.Fatal("menu request must want nav")
	}
}

func TestRankPathsForPageCreate_HeaderRequestStillPrefersHeader(t *testing.T) {
	// Non-redesign page create that explicitly mentions menu/header.
	paths := []string{
		"pages.json",
		"pages/home.liquid",
		"components/header.liquid",
		"components/css/header.css",
		"components/css/home.css",
	}
	got := rankPathsForPageCreate(paths, "create a Contact Us page and add it to the menu")
	joined := strings.Join(got, "|")
	if !strings.Contains(joined, "header") && !strings.Contains(joined, "menu") {
		t.Fatalf("menu request should include header/menu, got %v", got)
	}
}

func TestComplexPageBudgets_Bounded(t *testing.T) {
	if maxComplexPageModelCalls > 6 {
		t.Fatalf("complex page iteration budget too high: %d", maxComplexPageModelCalls)
	}
	if maxComplexHomeModelCalls > 8 {
		t.Fatalf("full-home iteration budget too high: %d", maxComplexHomeModelCalls)
	}
	if maxComplexExploration > 2 {
		t.Fatalf("complex exploration budget too high: %d", maxComplexExploration)
	}
	if maxComplexExploreStreak > 1 {
		t.Fatalf("complex explore streak too high: %d", maxComplexExploreStreak)
	}
	if simpleEditMaxTokens != 8000 {
		t.Fatalf("simple_edit max_tokens must stay 8000, got %d", simpleEditMaxTokens)
	}
	if ai.PreparedFirstTokenTimeout() > 90*time.Second {
		t.Fatalf("prepared first-token timeout too high: %v", ai.PreparedFirstTokenTimeout())
	}
	if ai.PreparedFirstTokenTimeout() < 5*time.Second {
		t.Fatalf("prepared first-token timeout too aggressive: %v", ai.PreparedFirstTokenTimeout())
	}
	if ai.PreparedStreamIdleTimeout() > 45*time.Second {
		t.Fatalf("prepared stream idle timeout too high: %v", ai.PreparedStreamIdleTimeout())
	}
	if ai.PreparedStreamIdleTimeout() < 5*time.Second {
		t.Fatalf("prepared stream idle timeout too aggressive: %v", ai.PreparedStreamIdleTimeout())
	}
}

func TestRecentChatTurns_NoSummarize(t *testing.T) {
	turns := []ai.Turn{
		{Role: "user", Content: "1"},
		{Role: "assistant", Content: "2"},
		{Role: "user", Content: "3"},
		{Role: "assistant", Content: "4"},
		{Role: "user", Content: "5"},
		{Role: "assistant", Content: "6"},
	}
	got := recentChatTurns(turns, 4)
	if len(got) != 4 {
		t.Fatalf("got %d want 4", len(got))
	}
	if got[0].Content != "3" || got[3].Content != "6" {
		t.Fatalf("unexpected window: %+v", got)
	}
	if len(recentChatTurns(turns, 10)) != 6 {
		t.Fatal("n larger than len should return all")
	}
}

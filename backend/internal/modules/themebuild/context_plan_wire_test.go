package themebuild

import (
	"testing"

	"ai-chat/internal/ai"
	"ai-chat/internal/buildercontext"
	"ai-chat/internal/themefs"
)

func TestBuildContextPlan_RegisterBypassesDeepSeekSignals(t *testing.T) {
	t.Parallel()
	cp, _ := buildContextPlan("can you register the blog page", planObservation{})
	if !cp.Valid {
		t.Fatal(cp.FallbackReason)
	}
	if !cp.History.Focused || cp.History.MaxRecentTurns != 0 {
		t.Fatalf("history=%+v", cp.History)
	}
}

func TestApplyContextPlan_OmitsManifestAndStubs(t *testing.T) {
	t.Parallel()
	cp := buildercontext.ContextPlan{
		Valid:                true,
		OmitManifest:         true,
		OmitFullPagesJSON:    true,
		OmitFullDefaultsJSON: true,
		IncludeFileTree:      false,
		MaxFiles:             8,
	}
	real := &ai.ThemeContext{
		PagesJSON:    `[{"slug":"home","page":"home"},{"slug":"blog","page":"blog"},{"slug":"a"},{"slug":"b"},{"slug":"c"},{"slug":"d"},{"slug":"e"},{"slug":"f"}]`,
		DefaultsJSON: `{"colors":{"primary":"#000"},"menu":{"items":[{"label":"Home"},{"label":"Shop"},{"label":"About"},{"label":"Contact"}]}}`,
		Manifest:     &themefs.Manifest{Components: []themefs.ComponentInfo{{Path: "components/button.liquid"}}},
		FileTree:     []themefs.FileTreeEntry{{Path: "pages/home.liquid", Name: "home.liquid", Type: "file"}},
	}
	for len(real.PagesJSON) < 900 {
		real.PagesJSON += `,"pad":"x"`
	}
	applyContextPlanToThemeContext(real, cp, nil)
	if real.Manifest != nil {
		t.Fatal("manifest should be nil")
	}
	if real.FileTree != nil {
		t.Fatal("file tree should be cleared")
	}
	if len(real.PagesJSON) > 500 {
		t.Fatalf("pages.json not stubbed: %d", len(real.PagesJSON))
	}
}

func TestSelectTurnsForContextPlan(t *testing.T) {
	t.Parallel()
	prior := make([]ai.Turn, 30)
	for i := range prior {
		prior[i] = ai.Turn{Role: "user", Content: "t"}
	}
	cp := buildercontext.ContextPlan{
		Valid: true,
		History: buildercontext.HistoryPolicy{
			Focused: true, MaxRecentTurns: 2, SkipSummarize: true,
		},
	}
	turns, n, ok := selectTurnsForContextPlan(prior, cp)
	if !ok || n != 2 || len(turns) != 2 {
		t.Fatalf("ok=%v n=%d len=%d", ok, n, len(turns))
	}
}

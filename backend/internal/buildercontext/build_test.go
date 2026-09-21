package buildercontext

import (
	"strings"
	"testing"

	"ai-chat/internal/ai"
	"ai-chat/internal/builderplan"
)

func TestBuild_ButtonColorMinimal(t *testing.T) {
	t.Parallel()
	plan, err := builderplan.BuildPlan("change the header button color to blue")
	if err != nil {
		t.Fatal(err)
	}
	cp, meta := Build(plan)
	if !cp.Valid || meta.UsedFallback {
		t.Fatalf("plan=%+v meta=%+v", cp, meta)
	}
	all := cp.AllFiles()
	if len(all) > 4 {
		t.Fatalf("too many files: %v", all)
	}
	for _, f := range all {
		if strings.Contains(f, "blog") {
			t.Fatalf("button edit must not load blog pages: %v", all)
		}
	}
	if !cp.OmitManifest || !cp.OmitFullPagesJSON || !cp.History.Focused {
		t.Fatalf("expected omit manifest/pages + focused history: %+v", cp)
	}
}

func TestBuild_BlogSoftwareHouse(t *testing.T) {
	t.Parallel()
	plan, err := builderplan.BuildPlan("change the blogs according to software house and keep the JPRO meta titles")
	if err != nil {
		t.Fatal(err)
	}
	// Attach semantic protect like intelligence would.
	plan, err = builderplan.ApplySemanticRefinement(plan,
		[]string{"adapt blog content for software house audience"},
		[]string{"meta_title"},
		nil, "", false, 0.9, "test")
	if err != nil {
		t.Fatal(err)
	}
	cp, _ := Build(plan)
	if !cp.Valid {
		t.Fatal(cp.FallbackReason)
	}
	blob := strings.Join(cp.AllFiles(), " ")
	if !strings.Contains(blob, "blog") {
		t.Fatalf("expected blog focus, got %v", cp.AllFiles())
	}
	if cp.OmitFullPagesJSON {
		t.Fatal("protect meta titles requires pages.json schema")
	}
	if !cp.OmitManifest {
		t.Fatal("manifest should be omitted for blog rewrite")
	}
}

func TestBuild_Create2BlogPages(t *testing.T) {
	t.Parallel()
	plan, err := builderplan.BuildPlan("create 2 blog pages")
	if err != nil {
		t.Fatal(err)
	}
	cp, _ := Build(plan)
	if !cp.Valid {
		t.Fatal(cp.FallbackReason)
	}
	foundSchema := false
	for _, f := range cp.SchemaFiles {
		if f == "pages.json" {
			foundSchema = true
		}
	}
	if !foundSchema {
		t.Fatalf("create needs pages.json schema: %+v", cp)
	}
	if len(cp.AllFiles()) > 8 {
		t.Fatalf("over cap: %v", cp.AllFiles())
	}
}

func TestBuild_AmbiguousMinimal(t *testing.T) {
	t.Parallel()
	plan, err := builderplan.BuildPlan("make the site better")
	if err != nil {
		t.Fatal(err)
	}
	cp, _ := Build(plan)
	if !cp.Valid {
		t.Fatal("ambiguous should still be a valid minimal plan")
	}
	if len(cp.AllFiles()) != 0 || !cp.OmitManifest || cp.IncludeFileTree {
		t.Fatalf("ambiguous must not dump theme: %+v files=%v", cp, cp.AllFiles())
	}
}

func TestBuild_RegisterExisting(t *testing.T) {
	t.Parallel()
	plan, err := builderplan.BuildPlan("can you register the blog page")
	if err != nil {
		t.Fatal(err)
	}
	if builderplan.NeedsDeepSeek(plan) {
		t.Fatal("register must not need DeepSeek")
	}
	cp, _ := Build(plan)
	if !cp.History.Focused || cp.History.MaxRecentTurns != 0 {
		t.Fatalf("register history=%+v", cp.History)
	}
}

func TestSelectHistory_Focused(t *testing.T) {
	t.Parallel()
	turns := make([]ai.Turn, 40)
	for i := range turns {
		turns[i] = ai.Turn{Role: "user", Content: "x"}
	}
	out, n, applied := SelectHistory(turns, HistoryPolicy{Focused: true, MaxRecentTurns: 2, SkipSummarize: true})
	if !applied || n != 2 || len(out) != 2 {
		t.Fatalf("got n=%d len=%d applied=%v", n, len(out), applied)
	}
	out, n, applied = SelectHistory(turns, HistoryPolicy{Focused: true, MaxRecentTurns: 0, SkipSummarize: true})
	if !applied || n != 0 || out != nil {
		t.Fatalf("zero window: n=%d out=%v", n, out)
	}
}

func TestNarrowExisting_SafeSubset(t *testing.T) {
	t.Parallel()
	existing := []string{"pages/blog.liquid", "pages/css/blog.css", "pages/home.liquid"}
	got, ok := NarrowExisting(existing, []string{"pages/blog.liquid", "pages/css/blog.css"})
	if !ok || len(got) != 2 {
		t.Fatalf("got=%v ok=%v", got, ok)
	}
	got, ok = NarrowExisting(existing, []string{"pages/missing.liquid"})
	if ok {
		t.Fatalf("should not narrow on missing: %v", got)
	}
}

func TestValidate_RejectsTraversal(t *testing.T) {
	t.Parallel()
	cp := ContextPlan{
		Valid:        true,
		MaxFiles:     8,
		PrimaryFiles: []string{"../etc/passwd"},
	}
	if err := Validate(cp); err == nil {
		t.Fatal("expected rejection")
	}
}

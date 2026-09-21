package builderintelligence_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"ai-chat/internal/builderintelligence"
	"ai-chat/internal/builderintelligence/providers"
	"ai-chat/internal/builderplan"
)

func TestShouldCall_SkipSimpleRegisterAndCreate(t *testing.T) {
	t.Parallel()
	for _, p := range []string{
		"change button color to blue",
		"can you register the blog page",
		"create 2 blog pages",
	} {
		plan, err := builderplan.BuildPlan(p)
		if err != nil {
			t.Fatal(err)
		}
		if builderintelligence.ShouldCall(plan).Call {
			t.Fatalf("%q must skip local LM, reason would call", p)
		}
	}
}

func TestShouldCall_CallSemanticCases(t *testing.T) {
	t.Parallel()
	for _, p := range []string{
		"make the site better",
		"change blogs for a software house but keep JPRO meta titles",
		"make the blog more professional for SaaS customers and don't change its slug",
		"change the blog title but leave its meta description and slug alone",
	} {
		plan, err := builderplan.BuildPlan(p)
		if err != nil {
			t.Fatal(err)
		}
		if !builderintelligence.ShouldCall(plan).Call {
			t.Fatalf("%q should call local semantic extraction", p)
		}
	}
}

func TestService_DisabledSkips(t *testing.T) {
	t.Parallel()
	svc := builderintelligence.New(builderintelligence.Config{Enabled: false})
	base, _ := builderplan.BuildPlan("change blogs keep JPRO meta titles")
	out := svc.Understand(context.Background(), builderintelligence.Input{DeterministicPlan: base})
	if !out.Meta.Skipped || out.Meta.Reason != "disabled" {
		t.Fatalf("meta=%+v", out.Meta)
	}
}

func TestService_HeuristicJPRO(t *testing.T) {
	t.Parallel()
	svc := builderintelligence.New(builderintelligence.Config{
		Enabled: true, Provider: builderintelligence.ProviderHeuristic, Timeout: time.Second,
	})
	base, err := builderplan.BuildPlan("change blogs for a software house but keep JPRO meta titles")
	if err != nil {
		t.Fatal(err)
	}
	opsBefore := len(base.Operations)
	intentBefore := base.Intent
	out := svc.Understand(context.Background(), builderintelligence.Input{
		Prompt: base.OriginalPrompt, DeterministicPlan: base,
	})
	if out.Meta.Failure || out.Meta.Skipped || !out.Meta.RefinementApplied {
		t.Fatalf("meta=%+v", out.Meta)
	}
	if out.Plan.Intent != intentBefore || len(out.Plan.Operations) != opsBefore {
		t.Fatal("deterministic intent/ops must stay authoritative")
	}
	blob := strings.Join(out.Plan.Constraints, " ")
	if !strings.Contains(blob, "jpro") && !strings.Contains(blob, "protect_field:meta_title") {
		t.Fatalf("constraints=%v", out.Plan.Constraints)
	}
	if !strings.Contains(blob, "protect_field:meta_title") {
		t.Fatalf("want protect_field:meta_title in %v", out.Plan.Constraints)
	}
}

func TestService_Create2Skips(t *testing.T) {
	t.Parallel()
	svc := builderintelligence.New(builderintelligence.Config{
		Enabled: true, Provider: builderintelligence.ProviderHeuristic,
	})
	base, _ := builderplan.BuildPlan("create 2 blog pages")
	out := svc.Understand(context.Background(), builderintelligence.Input{DeterministicPlan: base})
	if !out.Meta.Skipped || out.Meta.Called {
		t.Fatalf("create 2 must skip meta=%+v", out.Meta)
	}
}

func TestService_UnavailableFallback(t *testing.T) {
	t.Parallel()
	svc := builderintelligence.New(builderintelligence.Config{
		Enabled: true, Provider: builderintelligence.ProviderHTTP, // empty URL
	})
	base, _ := builderplan.BuildPlan("change blogs keep JPRO meta titles")
	out := svc.Understand(context.Background(), builderintelligence.Input{DeterministicPlan: base})
	if !out.Meta.Failure || out.Meta.Reason != "unavailable" {
		t.Fatalf("meta=%+v", out.Meta)
	}
	if out.Plan.Intent != base.Intent {
		t.Fatal("deterministic must survive")
	}
}

func TestApplySemantic_IgnoresSmuggledOps(t *testing.T) {
	t.Parallel()
	base, _ := builderplan.BuildPlan("change blogs keep JPRO meta titles")
	out, err := builderplan.ApplySemanticRefinement(base,
		[]string{"preserve titles", "create_page now", "pages/evil.liquid"},
		[]string{"meta_title", "../../etc/passwd"},
		[]string{"professional"},
		"", false, 0.9, "test",
	)
	if err != nil {
		t.Fatal(err)
	}
	blob := strings.Join(out.Constraints, " ")
	if strings.Contains(blob, "create_page") || strings.Contains(blob, "pages/evil") || strings.Contains(blob, "passwd") {
		t.Fatalf("smuggled content leaked: %v", out.Constraints)
	}
	if out.Intent != base.Intent {
		t.Fatal("intent must not change")
	}
}

func TestHeuristicExtract(t *testing.T) {
	t.Parallel()
	ref, err := (providers.Heuristic{}).Extract(context.Background(), providers.Input{
		Prompt: "make the blog more professional for SaaS customers and don't change its slug",
		Plan:   providers.MiniPlanHint{Intent: "full_page", Operations: []string{"update_page_content"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	foundSlug := false
	for _, f := range ref.ProtectedFields {
		if f == "slug" {
			foundSlug = true
		}
	}
	if !foundSlug {
		t.Fatalf("want slug protected: %+v", ref)
	}
}

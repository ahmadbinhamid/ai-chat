package builderplan_test

import (
	"testing"

	"ai-chat/internal/builderplan"
)

func TestClassify_VagueImprovePageIsAmbiguous(t *testing.T) {
	t.Parallel()
	for _, p := range []string{
		"improve the page",
		"optimize the site",
		"make the site better",
		"make it more professional",
	} {
		c := builderplan.DeterministicClassifier{}.Classify(p)
		if c.Intent != builderplan.IntentAmbiguous {
			t.Fatalf("%q: intent=%s want ambiguous", p, c.Intent)
		}
	}
}

func TestClassify_ImproveBlogIntroductionStillContent(t *testing.T) {
	t.Parallel()
	c := builderplan.DeterministicClassifier{}.Classify("improve the blog introduction")
	if c.Intent == builderplan.IntentAmbiguous {
		t.Fatal("improve the blog introduction must not be ambiguous")
	}
	plan, err := builderplan.BuildPlan("improve the blog introduction")
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range plan.Operations {
		if op.Kind == builderplan.OpCreatePage {
			t.Fatalf("must not create_page: %v", plan.Operations)
		}
	}
}

func TestClassify_AddExistingPageToNavNotCreate(t *testing.T) {
	t.Parallel()
	plan, err := builderplan.BuildPlan("add the existing contact page to navigation")
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range plan.Operations {
		if op.Kind == builderplan.OpCreatePage {
			t.Fatalf("must not create_page for existing contact nav: %v", plan.Operations)
		}
	}
	hasNav := false
	for _, op := range plan.Operations {
		if op.Kind == builderplan.OpAddToNavigation || op.Kind == builderplan.OpRegisterExistingPage {
			hasNav = true
		}
	}
	if !hasNav {
		t.Fatalf("expected nav/register op, got %v", plan.Operations)
	}
}

func TestClassify_RewritePagesJSONRejectedAmbiguous(t *testing.T) {
	t.Parallel()
	c := builderplan.DeterministicClassifier{}.Classify("rewrite pages.json completely")
	if c.Intent != builderplan.IntentAmbiguous {
		t.Fatalf("intent=%s want ambiguous (destructive short-circuit)", c.Intent)
	}
	plan, err := builderplan.BuildPlan("rewrite pages.json completely")
	if err != nil {
		t.Fatal(err)
	}
	if builderplan.NeedsDeepSeek(plan) {
		t.Fatal("destructive pages.json rewrite must not need DeepSeek")
	}
}

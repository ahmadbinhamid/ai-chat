package builderplan_test

import (
	"testing"

	"ai-chat/internal/builderplan"
)

func TestClassify_PageTroubleshootNotAmbiguous(t *testing.T) {
	t.Parallel()
	prompts := []string{
		"blog page is not working please check and fix it",
		"blog page is not open now why?",
		"the blog page is not working please check and fix it",
		"why is the pricing page not opening?",
		"fix the blog page",
		"now it is not working",
	}
	for _, p := range prompts {
		c := builderplan.DeterministicClassifier{}.Classify(p)
		if c.Intent != builderplan.IntentPageTroubleshoot {
			t.Fatalf("%q: intent=%s want page_troubleshoot", p, c.Intent)
		}
		plan, err := builderplan.BuildPlan(p)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Ambiguous {
			t.Fatalf("%q: must not be ambiguous", p)
		}
		if builderplan.NeedsDeepSeek(plan) {
			t.Fatalf("%q: diagnose must not need DeepSeek", p)
		}
		hasDiag := false
		for _, op := range plan.Operations {
			if op.Kind == builderplan.OpDiagnoseExistingPage {
				hasDiag = true
			}
			if op.Kind == builderplan.OpClarify {
				t.Fatalf("%q: must not clarify", p)
			}
			if op.Kind == builderplan.OpCreatePage {
				t.Fatalf("%q: must not create_page", p)
			}
		}
		if !hasDiag {
			t.Fatalf("%q: expected diagnose_existing_page, ops=%v", p, plan.Operations)
		}
	}
}

func TestClassify_MakeSiteBetterStillAmbiguous(t *testing.T) {
	t.Parallel()
	c := builderplan.DeterministicClassifier{}.Classify("make the site better")
	if c.Intent != builderplan.IntentAmbiguous {
		t.Fatalf("intent=%s", c.Intent)
	}
}

func TestBuildPlan_BlogTroubleshootTarget(t *testing.T) {
	t.Parallel()
	plan, err := builderplan.BuildPlan("blog page is not working please check and fix it")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Operations[0].Target != "pages/blog.liquid" {
		t.Fatalf("target=%q", plan.Operations[0].Target)
	}
}

package builderplan_test

import (
	"strings"
	"testing"

	"ai-chat/internal/builderplan"
)

func TestBuildPlan_BlogProfessionalNotCreatePage(t *testing.T) {
	t.Parallel()
	prompts := []string{
		"make the blog more professional for SaaS customers but don't change the slug",
		"update the blog content but keep the slug",
		"rewrite this existing blog for software companies",
	}
	for _, p := range prompts {
		plan, err := builderplan.BuildPlan(p)
		if err != nil {
			t.Fatalf("%q: %v", p, err)
		}
		if plan.Intent == builderplan.IntentPageCreate {
			t.Fatalf("%q: intent=page_create (must be content update)", p)
		}
		for _, op := range plan.Operations {
			if op.Kind == builderplan.OpCreatePage {
				t.Fatalf("%q: must not include create_page, got %v", p, plan.Operations)
			}
		}
		hasContent := false
		for _, op := range plan.Operations {
			if op.Kind == builderplan.OpUpdatePageContent || op.Kind == builderplan.OpFullPageEdit {
				hasContent = true
			}
		}
		if !hasContent {
			t.Fatalf("%q: expected update_page_content, intent=%s ops=%v", p, plan.Intent, plan.Operations)
		}
	}

	plan, err := builderplan.BuildPlan("make the blog more professional for SaaS customers but don't change the slug")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range plan.Constraints {
		if strings.Contains(c, "protect_field:slug") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected protect_field:slug constraint, got %v", plan.Constraints)
	}
}

func TestBuildPlan_MakeNewBlogPageStillCreate(t *testing.T) {
	t.Parallel()
	plan, err := builderplan.BuildPlan("make a new blog page")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Intent != builderplan.IntentPageCreate && plan.Intent != builderplan.IntentCompound {
		t.Fatalf("intent=%s want page_create/compound", plan.Intent)
	}
	hasCreate := false
	for _, op := range plan.Operations {
		if op.Kind == builderplan.OpCreatePage {
			hasCreate = true
		}
	}
	if !hasCreate {
		t.Fatalf("expected create_page, ops=%v", plan.Operations)
	}
}

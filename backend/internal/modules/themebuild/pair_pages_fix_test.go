package themebuild

import (
	"strings"
	"testing"

	"ai-chat/internal/ai"
	"ai-chat/internal/themefs"
)

func TestRequestedNewPageCount_PairOfServicePages(t *testing.T) {
	t.Parallel()
	cases := []struct {
		prompt string
		want   int
	}{
		{"create pair of service pages please", 2},
		{"create a pair of pages", 2},
		{"create two service pages", 2},
		{"create 2 blog pages", 2},
		{"create three pages", 3},
		{"create a page", 0},
	}
	for _, tc := range cases {
		if got := requestedNewPageCount(tc.prompt); got != tc.want {
			t.Fatalf("%q: count=%d want %d", tc.prompt, got, tc.want)
		}
	}
	plan, ok := PlanCompoundWorkflow("create pair of service pages please")
	if !ok {
		t.Fatal("pair of service pages must use compound workflow")
	}
	creates := 0
	for _, s := range plan.Steps {
		if s.Kind == CompoundStepCreatePage {
			creates++
		}
	}
	if creates != 2 {
		t.Fatalf("create steps=%d want 2 (got %#v)", creates, plan.Steps)
	}
}

func TestSynthesizeMissingPageRegistry_SingleCreate(t *testing.T) {
	t.Parallel()
	r := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "pages/crm.liquid", Action: "create", Content: "{% layout-start %}ok{% layout-end %}"},
	}}
	if !synthesizeMissingPageRegistry(r) {
		t.Fatal("expected synthesize")
	}
	if r.PageRegistryEntry == nil || r.PageRegistryEntry.Page != "crm" {
		t.Fatalf("entry=%+v", r.PageRegistryEntry)
	}
	if err := ensureProposedCreatesRegistered(r); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureProposedCreatesRegistered_MultiWithoutPagesJSON(t *testing.T) {
	t.Parallel()
	result := &ai.Result{
		Files: []ai.GeneratedFile{
			{Path: "pages/a.liquid", Action: "create", Content: "a"},
			{Path: "pages/b.liquid", Action: "create", Content: "b"},
		},
		PageRegistryEntry: &themefs.PageEntry{Page: "a", Slug: "a", Type: "custom", Path: "/pages", Status: "published"},
	}
	if synthesizeMissingPageRegistry(result) {
		t.Fatal("must not synthesize when registry already set")
	}
	result.PageRegistryEntry = nil
	if synthesizeMissingPageRegistry(result) {
		t.Fatal("must not synthesize for multi-create")
	}
	err := ensureProposedCreatesRegistered(result)
	if err == nil || !strings.Contains(err.Error(), "compound") {
		t.Fatalf("want multi-create error, got %v", err)
	}
}

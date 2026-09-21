package themebuild

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ai-chat/internal/ai"
	"ai-chat/internal/genfail"
	"ai-chat/internal/themefs"
)

func TestPlanCompoundWorkflow_Create2BlogPages(t *testing.T) {
	prompts := []string{
		"can you help me to create 2 blog pages",
		"can you create 2 blog pages for a software company related and add in pages ?",
		"create 3 blog pages",
	}
	for _, p := range prompts {
		plan, ok := PlanCompoundWorkflow(p)
		if !ok {
			t.Fatalf("%q: expected compound plan", p)
		}
		creates := 0
		for _, s := range plan.Steps {
			if s.Kind == CompoundStepCreatePage {
				creates++
			}
		}
		want := multiPageCreateBatchSize(p)
		if creates != want {
			t.Fatalf("%q: create steps=%d want %d (plan=%+v)", p, creates, want, plan.Steps)
		}
	}
}

func TestPlanCompoundWorkflow_PagePlusMenu(t *testing.T) {
	p := "create 2 blog pages and add them to the menu"
	plan, ok := PlanCompoundWorkflow(p)
	if !ok {
		t.Fatal("expected compound plan")
	}
	kinds := compoundStepKinds(plan)
	if len(kinds) < 3 {
		t.Fatalf("expected create+create+menu, got %v", kinds)
	}
	if kinds[len(kinds)-1] != CompoundStepAddToMenu {
		t.Fatalf("last step should be menu, got %v", kinds)
	}
}

func TestRejectOversizedMultiPageIndexRewrite_BlogLiquid(t *testing.T) {
	p := "can you help me to create 2 blog pages"
	result := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "pages/saas-tips.liquid", Action: "create", Content: "{% layout %}"},
		{Path: "pages/blog.liquid", Action: "update", Content: strings.Repeat("x", 2000)},
		{Path: "pages.json", Action: "update", Content: `[]`},
	}}
	if err := RejectOversizedMultiPageIndexRewrite(p, result); err == nil {
		t.Fatal("expected reject of blog.liquid rewrite")
	}
	stripped := StripMultiPageIndexRewrites(result)
	for _, f := range stripped.Files {
		if strings.EqualFold(f.Path, "pages/blog.liquid") {
			t.Fatal("blog.liquid should be stripped")
		}
	}
	if err := incompleteAtomicPageCreateProposal(stripped); err != nil {
		t.Fatalf("after strip, atomic create should pass with one liquid+json: %v", err)
	}
}

func TestMergeCompoundResults_PreservesPriorPage(t *testing.T) {
	page1 := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "pages/one.liquid", Action: "create", Content: "a"},
		{Path: "pages.json", Action: "update", Content: `[{"slug":"one"}]`},
	}}
	page2 := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "pages/two.liquid", Action: "create", Content: "b"},
		{Path: "pages.json", Action: "update", Content: `[{"slug":"one"},{"slug":"two"}]`},
	}}
	merged := MergeCompoundResults(page1, page2)
	if len(merged.Files) != 3 {
		t.Fatalf("files=%d want 3 (one, two, pages.json)", len(merged.Files))
	}
	var jsonBody string
	hasOne, hasTwo := false, false
	for _, f := range merged.Files {
		switch f.Path {
		case "pages/one.liquid":
			hasOne = true
		case "pages/two.liquid":
			hasTwo = true
		case "pages.json":
			jsonBody = f.Content
		}
	}
	if !hasOne || !hasTwo {
		t.Fatal("both page liquids must remain after merge")
	}
	if !strings.Contains(jsonBody, "two") {
		t.Fatalf("pages.json should be replaced by later step: %s", jsonBody)
	}
}

func TestCompoundPartialFailureMessage_Step2(t *testing.T) {
	progress := CompoundProgress{
		Completed: []CompoundStep{{Label: "Create page 1 of 2"}},
		Failed:    &CompoundStep{Label: "Create page 2 of 2"},
	}
	msg := CompoundPartialFailureMessage(progress, errors.New("the generated changes didn't pass validation after 2 attempts: x"))
	if !strings.Contains(msg, "Create page 1 of 2 succeeded") {
		t.Fatalf("missing page1 success: %q", msg)
	}
	if !strings.Contains(msg, "Create page 2 of 2 needs another attempt") {
		t.Fatalf("missing page2 failure: %q", msg)
	}
	if !strings.Contains(msg, "validation failed") {
		t.Fatalf("missing validation reason: %q", msg)
	}
	c := genfail.Classify(errors.New(msg))
	if c.Code != genfail.CodeCompoundPartial {
		t.Fatalf("code=%s want COMPOUND_PARTIAL", c.Code)
	}
	san := ai.SanitizeError(&errCompoundPartial{Msg: msg, Cause: context.DeadlineExceeded})
	if strings.Contains(san, "something went wrong") {
		t.Fatalf("generic sanitize: %q", san)
	}
}

func TestCompoundPartialFailureMessage_TimeoutStep2(t *testing.T) {
	progress := CompoundProgress{
		Completed: []CompoundStep{{Label: "Create page 1 of 2"}},
		Failed:    &CompoundStep{Label: "Create page 2 of 2"},
	}
	msg := CompoundPartialFailureMessage(progress, context.DeadlineExceeded)
	if !strings.Contains(strings.ToLower(msg), "timed out after 10 minutes") {
		t.Fatalf("expected parent timeout wording: %q", msg)
	}
	if !strings.Contains(msg, "Create page 1 of 2") {
		t.Fatalf("expected completed step preserved: %q", msg)
	}
}

func TestShouldPreserveProposalScope_MultiPageFalse(t *testing.T) {
	in := GenerateInput{Prompt: "can you help me to create 2 blog pages"}
	tc := ai.ThemeContext{PageCreatePrepared: true}
	if shouldPreserveProposalScope(in, tc) {
		t.Fatal("multi-page create must not preserve scope (avoids blog.liquid churn)")
	}
}

func TestIncompleteAtomicPageCreate_AcceptsRegistryEntry(t *testing.T) {
	err := incompleteAtomicPageCreateProposal(&ai.Result{
		Files: []ai.GeneratedFile{
			{Path: "pages/saas-tips.liquid", Action: "create", Content: "x"},
		},
		PageRegistryEntry: &themefs.PageEntry{Slug: "saas-tips", Page: "saas-tips", Status: "published"},
	})
	if err != nil {
		t.Fatalf("page_registry_entry must satisfy atomic create: %v", err)
	}
}

func TestIncompleteAtomicPageCreate_RejectsTwoCreates(t *testing.T) {
	err := incompleteAtomicPageCreateProposal(&ai.Result{Files: []ai.GeneratedFile{
		{Path: "pages/a.liquid", Action: "create", Content: "x"},
		{Path: "pages/b.liquid", Action: "create", Content: "y"},
		{Path: "pages.json", Action: "update", Content: `[]`},
	}})
	if err == nil {
		t.Fatal("expected reject of 2 creates in atomic step")
	}
}

func TestCompoundAtomic_RegistryAcceptedWithoutPagesJSON(t *testing.T) {
	// Simulates Step 1 of "create 2 blog pages": model emits registry, not pages.json.
	step := &ai.Result{
		Files: []ai.GeneratedFile{
			{Path: "pages/saas-tips.liquid", Action: "create", Content: "{% render 'liquid/layout-start' %}hi{% render 'liquid/layout-end' %}"},
		},
		PageRegistryEntry: &themefs.PageEntry{
			Title: "SaaS Tips", Slug: "saas-tips", Page: "saas-tips", Type: "custom",
			Path: "/pages", Status: "published",
		},
	}
	if err := incompleteAtomicPageCreateProposal(step); err != nil {
		t.Fatalf("atomic gate: %v", err)
	}
	// Batch multi-page gate must NOT run against the original N-page prompt
	// during compound atomic steps — that was the live Step-1 failure.
	batchErr := incompleteMultiPageCreateProposal(
		"can you create 2 blog pages for a software company related and add in pages ?",
		step,
	)
	if batchErr == nil {
		t.Fatal("single-shot batch gate should still reject registry-only for N=2")
	}
	if !strings.Contains(batchErr.Error(), "proposal/tool contract mismatch") &&
		!strings.Contains(batchErr.Error(), "page_registry_entry only registers") {
		t.Fatalf("expected contract-mismatch wording, got: %v", batchErr)
	}
	c := genfail.Classify(fmt.Errorf("invalid model proposal: %w", batchErr))
	if c.Code != genfail.CodeProposalContractMismatch {
		t.Fatalf("code=%s want PROPOSAL_CONTRACT_MISMATCH", c.Code)
	}
}

func TestMergeCompoundResults_RegistryEntriesAccumulate(t *testing.T) {
	existingPages := `[{"slug":"home","page":"home","type":"home","status":"published"}]`
	page1 := &ai.Result{
		Files: []ai.GeneratedFile{
			{Path: "pages/one.liquid", Action: "create", Content: "a"},
		},
		PageRegistryEntry: &themefs.PageEntry{Slug: "one", Page: "one", Type: "custom", Status: "published", Path: "/pages"},
	}
	page2 := &ai.Result{
		Files: []ai.GeneratedFile{
			{Path: "pages/two.liquid", Action: "create", Content: "b"},
		},
		PageRegistryEntry: &themefs.PageEntry{Slug: "two", Page: "two", Type: "custom", Status: "published", Path: "/pages"},
	}
	merged := MergeCompoundResults(page1, page2)
	if len(merged.Files) != 2 {
		t.Fatalf("files=%d want 2 liquids (no pages.json rewrite)", len(merged.Files))
	}
	for _, f := range merged.Files {
		if strings.EqualFold(f.Path, "pages.json") {
			t.Fatal("merge must not invent a pages.json rewrite when steps used page_registry_entry")
		}
	}
	if merged.PageRegistryEntry == nil || merged.PageRegistryEntry.Slug != "two" {
		t.Fatalf("latest registry should be on result: %+v", merged.PageRegistryEntry)
	}

	regs := accumulateCompoundRegistry(nil, page1.PageRegistryEntry)
	regs = accumulateCompoundRegistry(regs, page2.PageRegistryEntry)
	if len(regs) != 2 {
		t.Fatalf("registries=%d want 2", len(regs))
	}
	extras := compoundExtraRegistryEntries(merged, regs)
	if len(extras) != 1 || extras[0].Slug != "one" {
		t.Fatalf("extras should be prior page only: %+v", extras)
	}

	// Context merge preserves home + both new pages without model rewrite.
	updated := upsertPagesJSONWithRegistry(existingPages, page1.PageRegistryEntry)
	updated = upsertPagesJSONWithRegistry(updated, page2.PageRegistryEntry)
	if !strings.Contains(updated, `"home"`) || !strings.Contains(updated, `"one"`) || !strings.Contains(updated, `"two"`) {
		t.Fatalf("existing + new pages not preserved: %s", updated)
	}
}

func TestCompoundPartial_Page1CheckpointSurvivesPage2Failure(t *testing.T) {
	page1 := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "pages/one.liquid", Action: "create", Content: "a"},
	}, PageRegistryEntry: &themefs.PageEntry{Slug: "one", Page: "one"}}
	progress := CompoundProgress{
		Completed:  []CompoundStep{{Label: "Create page 1 of 2", Kind: CompoundStepCreatePage}},
		Failed:     &CompoundStep{Label: "Create page 2 of 2", Kind: CompoundStepCreatePage},
		Accum:      page1,
		Registries: []*themefs.PageEntry{page1.PageRegistryEntry},
	}
	cause := fmt.Errorf("proposal/tool contract mismatch: incomplete atomic page create: x")
	msg := CompoundPartialFailureMessage(progress, cause)
	if !strings.Contains(msg, "Create page 1 of 2 succeeded") {
		t.Fatalf("missing checkpoint wording: %q", msg)
	}
	if !strings.Contains(msg, "proposal/tool contract mismatch") {
		t.Fatalf("expected contract mismatch classification in message: %q", msg)
	}
	_, _, regs, err := compoundPartialOrErr(progress, nil, `[{"slug":"home","page":"home"}]`, cause)
	var partial *errCompoundPartial
	if !errors.As(err, &partial) {
		t.Fatalf("want errCompoundPartial, got %T %v", err, err)
	}
	if partial.Accum == nil || len(partial.Accum.Files) < 1 {
		t.Fatal("page 1 checkpoint must remain staged")
	}
	hasLiquid := false
	for _, f := range partial.Accum.Files {
		if f.Path == "pages/one.liquid" {
			hasLiquid = true
		}
	}
	if !hasLiquid {
		t.Fatal("page 1 liquid must remain after partial finalize")
	}
	if len(regs) != 1 || regs[0].Slug != "one" {
		t.Fatalf("page 1 registry must survive: %+v", regs)
	}
	c := genfail.Classify(err)
	if c.Code != genfail.CodeCompoundPartial {
		t.Fatalf("code=%s want COMPOUND_PARTIAL (step-scoped)", c.Code)
	}
}

func TestCompoundExtraRegistry_NoUnnecessaryPagesJSON(t *testing.T) {
	result := &ai.Result{
		Files: []ai.GeneratedFile{
			{Path: "pages/a.liquid", Action: "create", Content: "a"},
			{Path: "pages/b.liquid", Action: "create", Content: "b"},
		},
		PageRegistryEntry: &themefs.PageEntry{Slug: "b", Page: "b"},
	}
	regs := []*themefs.PageEntry{
		{Slug: "a", Page: "a"},
		{Slug: "b", Page: "b"},
	}
	extras := compoundExtraRegistryEntries(result, regs)
	if len(extras) != 1 {
		t.Fatalf("extras=%d want 1", len(extras))
	}
	// Staging path attaches PageMeta — files list must stay liquid-only.
	for _, f := range result.Files {
		if strings.EqualFold(f.Path, "pages.json") {
			t.Fatal("must not require pages.json file in proposal")
		}
	}
}

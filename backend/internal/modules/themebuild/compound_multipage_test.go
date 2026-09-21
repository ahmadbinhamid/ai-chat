package themebuild

import (
	"fmt"
	"strings"
	"testing"

	"ai-chat/internal/ai"
	"ai-chat/internal/genfail"
)

// TestMultiPageCreate_PreparedPromptMustNotTriggerPrivacyRewrite reproduces
// generation ed85fff1: merchant asked for 2 blog pages, but prepared context
// listed pages/privacy.liquid and the named-page gate rejected the proposal.
func TestMultiPageCreate_PreparedPromptMustNotTriggerPrivacyRewrite(t *testing.T) {
	merchant := "can you create 2 blog pages for a software company related and add in pages ?"
	prepared := complexPagePreparedPrompt(merchant, ComplexPageContext{
		Package: "## Pre-selected local MULTI-PAGE CREATE context\n" +
			"pages/privacy.liquid\npages/home.liquid\npages.json\n" +
			"software company content\n",
		Paths:      []string{"pages.json", "pages/privacy.liquid", "pages/home.liquid"},
		Sufficient: true,
	})
	if !strings.Contains(prepared, "privacy") {
		t.Fatal("test setup: prepared prompt must mention privacy")
	}
	if promptNamedPageSlug(merchant) != "" {
		t.Fatalf("merchant prompt must not name a page, got %q", promptNamedPageSlug(merchant))
	}
	if promptNamedPageSlug(prepared) != "privacy" {
		t.Fatal("prepared prompt should still falsely match privacy — proving the hazard")
	}

	// Gates must use merchant prompt, not prepared.
	result := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "pages/saas-tips.liquid", Action: "create", Content: "{% layout %}"},
		{Path: "pages/devops-guide.liquid", Action: "create", Content: "{% layout %}"},
		{Path: "pages.json", Action: "update", Content: `[{"slug":"home"},{"slug":"saas-tips"},{"slug":"devops-guide"}]`},
	}}
	if err := incompleteNamedPageRewriteProposal(merchant, result); err != nil {
		t.Fatalf("merchant multi-page must not hit named-page gate: %v", err)
	}
	if err := incompleteNamedPageRewriteProposal(prepared, result); err != nil {
		// Even if someone passes prepared by mistake, multi-page + isPageContentRewrite
		// should refuse once isMultiPageCreate is true on the string... prepared
		// still contains "create 2 blog pages" at the end after ---.
		t.Logf("prepared-only named-page err (ok if multi-page detected): %v", err)
	}
	// Full prepared string still contains merchant text after --- so multi-page is true.
	if err := incompleteNamedPageRewriteProposal(prepared, result); err != nil {
		t.Fatalf("prepared prompt still includes merchant multi-page ask — named-page must no-op: %v", err)
	}
	if err := incompleteMultiPageCreateProposal(merchant, result); err != nil {
		t.Fatalf("complete 2-page proposal rejected: %v", err)
	}
}

func TestMultiPageCreate_IncompleteClassifiesForMerchant(t *testing.T) {
	err := fmt.Errorf("invalid model proposal: %w",
		fmt.Errorf("incomplete multi-page create (need 2 pages): missing pages.json"))
	msg := ai.SanitizeError(err)
	if strings.Contains(msg, "something went wrong") {
		t.Fatalf("must not be generic: %q", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "pages") {
		t.Fatalf("expected pages-related message, got %q", msg)
	}
	c := genfail.Classify(err)
	if c.Code != genfail.CodeIncompleteMultiPage {
		t.Fatalf("code=%s want INCOMPLETE_MULTI_PAGE", c.Code)
	}
}

func TestIsPageContentRewrite_ExcludesMultiPageCreate(t *testing.T) {
	p := "can you create 2 blog pages for a software company related and add in pages ?"
	if isPageContentRewritePrompt(p) {
		t.Fatal("multi-page create must not classify as page content rewrite")
	}
}

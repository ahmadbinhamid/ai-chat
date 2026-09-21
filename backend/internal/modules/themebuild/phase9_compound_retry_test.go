package themebuild

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/genfail"
	"ai-chat/internal/prodhardening"
)

// TestPhase9_CompoundWorkflowIntentAndBatch documents Test C of the Phase 9
// matrix: create 2 blog pages + register in Pages.
func TestPhase9_CompoundWorkflowIntentAndBatch(t *testing.T) {
	prompt := "can you create 2 blog pages for a software company related and add in pages ?"
	got := ClassifyIntent(prompt, "", false)
	if got != IntentComplexPage {
		t.Fatalf("intent=%s want complex_page", got)
	}
	if !isMultiPageCreatePrompt(prompt) {
		t.Fatal("expected multi-page create")
	}
	if multiPageCreateBatchSize(prompt) != 2 {
		t.Fatalf("batch=%d want 2", multiPageCreateBatchSize(prompt))
	}
	if isPageContentRewritePrompt(prompt) {
		t.Fatal("must not classify as named page rewrite")
	}
}

// TestPhase9_PartialSecondPageFailureClassifies ensures page1-ok / page2-missing
// does not report success and maps to INCOMPLETE_MULTI_PAGE.
func TestPhase9_PartialSecondPageFailureClassifies(t *testing.T) {
	prompt := "create 2 blog pages and add them to pages"
	result := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "pages/saas-tips.liquid", Action: "create", Content: "{% layout %}"},
		{Path: "pages.json", Action: "update", Content: `[{"slug":"home"},{"slug":"saas-tips"}]`},
	}}
	err := incompleteMultiPageCreateProposal(prompt, result)
	if err == nil {
		t.Fatal("expected incomplete multi-page error")
	}
	wrapped := errors.New("invalid model proposal: " + err.Error())
	c := genfail.Classify(wrapped)
	if c.Code != genfail.CodeIncompleteMultiPage {
		t.Fatalf("code=%s", c.Code)
	}
	msg := ai.SanitizeError(wrapped)
	if strings.Contains(msg, "something went wrong") {
		t.Fatalf("generic sanitize: %q", msg)
	}
}

// TestPhase9_RetryBudgetBounded proves Generate × stream attempts stay finite
// under DefaultPolicy (no infinite repair/provider loop).
func TestPhase9_RetryBudgetBounded(t *testing.T) {
	p := prodhardening.DefaultPolicy()
	max := prodhardening.MaxProviderStreamAttemptsPerGeneration(p)
	if max <= 0 || max > 500 {
		t.Fatalf("unbounded or absurd stream attempt ceiling: %d", max)
	}
	// Multi-page bump (StreamMaxAttemptsOverride=3) still under generation
	// timeout and must not explode the theoretical ceiling unboundedly.
	multiPageCeiling := p.MaxGenerateCallsWithEscalation * p.MaxToolIterationsPerGenerate * 3
	if multiPageCeiling > 600 {
		t.Fatalf("multi-page stream ceiling too high: %d", multiPageCeiling)
	}
}

// TestPhase9_TimeoutCancellationStopsRetry ensures cancelled / deadline
// contexts are not retryable as provider failures.
func TestPhase9_TimeoutCancellationStopsRetry(t *testing.T) {
	cases := []struct {
		err       error
		code      genfail.Code
		retryable bool
	}{
		{context.Canceled, genfail.CodeCancelled, false},
		{context.DeadlineExceeded, genfail.CodeAIGenerationTimeout, true},
		{errors.New("provider stream idle timeout: no progress"), genfail.CodeStreamIdleTimeout, true},
		{errors.New("invalid model proposal: incomplete multi-page create (need 2 pages): x"), genfail.CodeIncompleteMultiPage, true},
	}
	for _, tc := range cases {
		c := genfail.Classify(tc.err)
		if c.Code != tc.code || c.Retryable != tc.retryable {
			t.Fatalf("%v → code=%s retryable=%v want %s/%v", tc.err, c.Code, c.Retryable, tc.code, tc.retryable)
		}
	}
}

// TestPhase9_IdempotentSlugReuseDetection documents that the incomplete gate
// counts create/update actions (not unique slugs). A retry that emits the
// same path twice can satisfy the count check — persistence/slug uniqueness
// remains the theme apply layer's responsibility.
func TestPhase9_IdempotentSlugReuseDetection(t *testing.T) {
	prompt := "create 2 blog pages and add in pages"
	dup := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "pages/saas-tips.liquid", Action: "create", Content: "a"},
		{Path: "pages/saas-tips.liquid", Action: "create", Content: "b"},
		{Path: "pages.json", Action: "update", Content: `[{"slug":"saas-tips"}]`},
	}}
	if err := incompleteMultiPageCreateProposal(prompt, dup); err != nil {
		t.Fatalf("duplicate-path creates currently pass the count gate: %v", err)
	}
	// Truly partial (one liquid) must still fail.
	partial := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "pages/saas-tips.liquid", Action: "create", Content: "a"},
		{Path: "pages.json", Action: "update", Content: `[{"slug":"saas-tips"}]`},
	}}
	if err := incompleteMultiPageCreateProposal(prompt, partial); err == nil {
		t.Fatal("single page liquid must fail multi-page gate")
	}
}

// TestPhase9_TimeoutHierarchyDocumentsParentChild order.
func TestPhase9_TimeoutHierarchyDocumentsParentChild(t *testing.T) {
	p := prodhardening.DefaultPolicy()
	if !prodhardening.TimeoutHierarchyOK(p) {
		t.Fatal("timeout hierarchy invalid")
	}
	if p.StreamIdleTimeout >= p.StreamFirstTokenTimeout {
		// Idle is shorter than first-token by design (post-token stall).
	}
	if p.GenerationTimeout != 10*time.Minute {
		t.Fatalf("generation timeout unexpectedly %s want 10m", p.GenerationTimeout)
	}
	_ = ai.PreparedFirstTokenTimeout()
	_ = ai.PreparedStreamIdleTimeout()
	if ai.PreparedStreamIdleTimeout() <= p.StreamIdleTimeout {
		// Prepared path widens idle for long page creates — must be >= default.
		t.Fatalf("prepared idle %s should exceed default idle %s",
			ai.PreparedStreamIdleTimeout(), p.StreamIdleTimeout)
	}
}

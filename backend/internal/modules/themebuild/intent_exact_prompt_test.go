package themebuild

import (
	"strings"
	"testing"
)

func TestExactUserPrompt_Create2BlogPagesAndAddInPages(t *testing.T) {
	p := "can you create 2 blog pages for a software company related and add in pages ?"
	got := ClassifyIntent(p, "edit", false)
	low := strings.ToLower(strings.Join(strings.Fields(p), " "))
	t.Logf("intent=%s multi=%v count=%d batch=%d pageCreate=%v",
		got, isMultiPageCreatePrompt(p), requestedNewPageCount(p), multiPageCreateBatchSize(p), pageCreateRe.MatchString(low))
	if got != IntentComplexPage {
		t.Fatalf("expected complex_page, got %s", got)
	}
	if !isMultiPageCreatePrompt(p) {
		t.Fatal("expected multi-page create")
	}
	if multiPageCreateBatchSize(p) != 2 {
		t.Fatalf("batch=%d want 2", multiPageCreateBatchSize(p))
	}
}

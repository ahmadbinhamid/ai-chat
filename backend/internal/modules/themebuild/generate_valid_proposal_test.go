package themebuild

import (
	"context"
	"errors"
	"testing"

	"ai-chat/internal/ai"
)

// fakeGeneratorErr wraps fakeGenerator to additionally support returning a
// hard error from Generate on a given call — fakeGenerator (see
// check_and_repair_test.go) only ever returns results, never an error.
type fakeGeneratorErr struct {
	fakeGenerator
	errOnCall int // 1-indexed call number to fail on; 0 means never
	err       error
}

func (f *fakeGeneratorErr) Generate(ctx context.Context, tc ai.ThemeContext, turns []ai.Turn, prompt string, onDelta func(string), progress ai.ToolProgress, toolExec ai.ToolExecutor, readFile ai.FileReader) (*ai.Result, error) {
	f.calls++
	if f.calls == f.errOnCall {
		return nil, f.err
	}
	idx := f.calls - 1
	if idx >= len(f.results) {
		idx = len(f.results) - 1
	}
	return f.results[idx], nil
}

func TestGenerateValidProposal_SucceedsImmediately(t *testing.T) {
	fg := &fakeGenerator{results: []*ai.Result{goodResult()}}
	svc := &Service{gen: fg}
	in := GenerateInput{TenantID: 1, ThemeSlug: "demo"}

	got, turns, err := svc.generateValidProposal(context.Background(), ai.ThemeContext{}, nil, "make it nice", nil, nil, nil, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fg.calls != 1 {
		t.Errorf("expected exactly 1 Generate call, got %d", fg.calls)
	}
	if got.Summary != "good" {
		t.Errorf("unexpected result: %+v", got)
	}
	if len(turns) != 0 {
		t.Errorf("expected turns unchanged when the first attempt succeeds, got %+v", turns)
	}
}

func TestGenerateValidProposal_RetriesPastAnInvalidFirstReply(t *testing.T) {
	fg := &fakeGenerator{results: []*ai.Result{invalidResult(), goodResult()}}
	svc := &Service{gen: fg}
	in := GenerateInput{TenantID: 1, ThemeSlug: "demo"}

	got, turns, err := svc.generateValidProposal(context.Background(), ai.ThemeContext{}, nil, "make it nice", nil, nil, nil, in)
	if err != nil {
		t.Fatalf("expected the generation to recover after the invalid reply, got error: %v", err)
	}
	if fg.calls != 2 {
		t.Errorf("expected exactly 2 Generate calls (1 invalid + 1 good), got %d", fg.calls)
	}
	if got.Summary != "good" {
		t.Fatalf("expected the eventually-good result to be returned, got %+v", got)
	}
	if len(turns) == 0 {
		t.Errorf("expected the corrective exchange to be recorded in turns")
	}
}

func TestGenerateValidProposal_FailsCleanlyWhenBudgetExhausted(t *testing.T) {
	fg := &fakeGenerator{results: []*ai.Result{invalidResult()}} // every call comes back invalid
	svc := &Service{gen: fg}
	in := GenerateInput{TenantID: 1, ThemeSlug: "demo"}

	_, _, err := svc.generateValidProposal(context.Background(), ai.ThemeContext{}, nil, "make it nice", nil, nil, nil, in)
	if err == nil {
		t.Fatal("expected an error once the retry budget is exhausted")
	}
	if fg.calls != maxThemeCheckRetries+1 {
		t.Errorf("expected exactly %d Generate calls (1 original + %d retries), got %d",
			maxThemeCheckRetries+1, maxThemeCheckRetries, fg.calls)
	}
	if wantPrefix := "invalid model proposal: "; len(err.Error()) < len(wantPrefix) || err.Error()[:len(wantPrefix)] != wantPrefix {
		t.Errorf("expected error to be wrapped as %q..., got %q", wantPrefix, err.Error())
	}
}

func TestGenerateValidProposal_HardGenerateErrorIsNotRetried(t *testing.T) {
	wantErr := errors.New("api transport error")
	fg := &fakeGeneratorErr{errOnCall: 1, err: wantErr}
	svc := &Service{gen: fg}
	in := GenerateInput{TenantID: 1, ThemeSlug: "demo"}

	_, _, err := svc.generateValidProposal(context.Background(), ai.ThemeContext{}, nil, "make it nice", nil, nil, nil, in)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected the hard error to propagate unwrapped-ish immediately, got %v", err)
	}
	if fg.calls != 1 {
		t.Errorf("expected exactly 1 Generate call — a hard error must not be retried, got %d", fg.calls)
	}
}

// hallucinatedEmptyResult mimics the production bug this task exists to
// fix: needs_clarification false, an empty files array, and zero
// exploration tool calls — the model describing work it never did.
func hallucinatedEmptyResult(summary string) *ai.Result {
	return &ai.Result{Summary: summary, NeedsClarification: false, ExplorationToolCalls: 0}
}

// legitimateEmptyResult mimics a real "nothing to change" answer that
// followed actual exploration — must never be treated as a hallucination.
func legitimateEmptyResult(summary string) *ai.Result {
	return &ai.Result{Summary: summary, NeedsClarification: false, ExplorationToolCalls: 3}
}

func clarificationResult(summary string) *ai.Result {
	return &ai.Result{Summary: summary, NeedsClarification: true, ExplorationToolCalls: 0}
}

func TestGenerateValidProposal_UnexploredEmptyProposalRetriesThenSucceeds(t *testing.T) {
	fg := &fakeGenerator{results: []*ai.Result{
		hallucinatedEmptyResult("Redesigned the page with a new animated hero and sticky sidebar."),
		goodResult(),
	}}
	svc := &Service{gen: fg}
	in := GenerateInput{TenantID: 1, ThemeSlug: "demo"}

	got, turns, err := svc.generateValidProposal(context.Background(), ai.ThemeContext{}, nil, "redesign the page", nil, nil, nil, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fg.calls != 2 {
		t.Errorf("expected exactly 2 Generate calls (1 hallucinated + 1 real), got %d", fg.calls)
	}
	if got.Summary != "good" || len(got.Files) == 0 {
		t.Fatalf("expected the real, successful proposal to be used, got %+v", got)
	}
	if len(turns) == 0 {
		t.Errorf("expected the corrective exchange to be recorded in turns")
	}
}

// TestGenerateValidProposal_UnexploredEmptyProposalExhaustsRetriesFallbackMessage
// is the exact production bug: the model claims a redesign, proposes
// nothing, explores nothing, and does it again on retry. The generation
// must still succeed (fail-open) with an honest fallback message, not the
// model's fabricated summary and not a hard failure.
func TestGenerateValidProposal_UnexploredEmptyProposalExhaustsRetriesFallbackMessage(t *testing.T) {
	fg := &fakeGenerator{results: []*ai.Result{
		hallucinatedEmptyResult("Redesigned the page with a new animated hero and sticky sidebar."),
	}}
	svc := &Service{gen: fg}
	in := GenerateInput{TenantID: 1, ThemeSlug: "demo"}

	got, _, err := svc.generateValidProposal(context.Background(), ai.ThemeContext{}, nil, "redesign the page", nil, nil, nil, in)
	if err != nil {
		t.Fatalf("expected success (fail-open) even after every retry stays empty, got error: %v", err)
	}
	if fg.calls != maxThemeCheckRetries+1 {
		t.Errorf("expected exactly %d Generate calls (1 original + %d retries), got %d",
			maxThemeCheckRetries+1, maxThemeCheckRetries, fg.calls)
	}
	if got.Summary != emptyProposalFallbackSummary {
		t.Errorf("expected the honest fallback summary, got %q", got.Summary)
	}
	if proposalHasChanges(got) {
		t.Errorf("expected no changes in the final result, got %+v", got)
	}
}

// TestGenerateValidProposal_NeedsClarificationEmptyProposalAcceptedImmediately
// confirms the untouched edge case: needs_clarification:true with an empty
// files array is already the correct, valid shape — no retry, no summary
// replacement, even though ExplorationToolCalls is 0 here too.
func TestGenerateValidProposal_NeedsClarificationEmptyProposalAcceptedImmediately(t *testing.T) {
	fg := &fakeGenerator{results: []*ai.Result{clarificationResult("I can't do that here — it's outside theme editing.")}}
	svc := &Service{gen: fg}
	in := GenerateInput{TenantID: 1, ThemeSlug: "demo"}

	got, _, err := svc.generateValidProposal(context.Background(), ai.ThemeContext{}, nil, "unrelated question", nil, nil, nil, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fg.calls != 1 {
		t.Errorf("expected exactly 1 Generate call — needs_clarification:true must never retry, got %d", fg.calls)
	}
	if got.Summary != "I can't do that here — it's outside theme editing." {
		t.Errorf("expected the model's own summary preserved, got %q", got.Summary)
	}
}

// TestGenerateValidProposal_LegitimateEmptyAnswerAfterExplorationAcceptedImmediately
// is the case the distinguishing rule exists to protect: a model that
// explored real files before concluding there's nothing to change (the
// out_of_scope/unrelated_technical_question eval shape) must be accepted
// as-is, its own summary preserved — never replaced with the generic
// fallback message.
func TestGenerateValidProposal_LegitimateEmptyAnswerAfterExplorationAcceptedImmediately(t *testing.T) {
	fg := &fakeGenerator{results: []*ai.Result{legitimateEmptyResult("The footer already matches your request — no change needed.")}}
	svc := &Service{gen: fg}
	in := GenerateInput{TenantID: 1, ThemeSlug: "demo"}

	got, _, err := svc.generateValidProposal(context.Background(), ai.ThemeContext{}, nil, "update the footer", nil, nil, nil, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fg.calls != 1 {
		t.Errorf("expected exactly 1 Generate call — a legitimate empty answer after real exploration must not retry, got %d", fg.calls)
	}
	if got.Summary != "The footer already matches your request — no change needed." {
		t.Errorf("expected the model's own summary preserved, got %q", got.Summary)
	}
}

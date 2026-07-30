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

func (f *fakeGeneratorErr) Generate(ctx context.Context, tc ai.ThemeContext, turns []ai.Turn, prompt string, onDelta func(string), toolExec ai.ToolExecutor) (*ai.Result, error) {
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

	got, turns, err := svc.generateValidProposal(context.Background(), ai.ThemeContext{}, nil, "make it nice", nil, nil, in)
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

	got, turns, err := svc.generateValidProposal(context.Background(), ai.ThemeContext{}, nil, "make it nice", nil, nil, in)
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

	_, _, err := svc.generateValidProposal(context.Background(), ai.ThemeContext{}, nil, "make it nice", nil, nil, in)
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

	_, _, err := svc.generateValidProposal(context.Background(), ai.ThemeContext{}, nil, "make it nice", nil, nil, in)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected the hard error to propagate unwrapped-ish immediately, got %v", err)
	}
	if fg.calls != 1 {
		t.Errorf("expected exactly 1 Generate call — a hard error must not be retried, got %d", fg.calls)
	}
}

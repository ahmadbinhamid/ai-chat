package themebuild

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ai-chat/internal/ai"
)

// summarizingFakeGenerator is a generator whose Summarize call is
// independently controllable from fakeGenerator's Generate — used to test
// summarizeOldTurns in isolation.
type summarizingFakeGenerator struct {
	fakeGenerator
	summarizeCalls int
	summarizeErr   error
}

func (f *summarizingFakeGenerator) Summarize(_ context.Context, turns []ai.Turn) (string, error) {
	f.summarizeCalls++
	if f.summarizeErr != nil {
		return "", f.summarizeErr
	}
	return fmt.Sprintf("summary of %d turns", len(turns)), nil
}

func turnsOf(n int) []ai.Turn {
	turns := make([]ai.Turn, n)
	for i := range turns {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		turns[i] = ai.Turn{Role: role, Content: fmt.Sprintf("turn %d", i)}
	}
	return turns
}

// TestSummarizeOldTurns_UnderThresholdUnchanged confirms a chat with at most
// summarizeHistoryThreshold turns is left completely alone — no Summarize
// call, no synthetic turn.
func TestSummarizeOldTurns_UnderThresholdUnchanged(t *testing.T) {
	fg := &summarizingFakeGenerator{}
	turns := turnsOf(summarizeHistoryThreshold)

	got := summarizeOldTurns(context.Background(), fg, turns)

	if fg.summarizeCalls != 0 {
		t.Errorf("expected no Summarize call for %d turns, got %d calls", len(turns), fg.summarizeCalls)
	}
	if len(got) != len(turns) {
		t.Fatalf("expected turns unchanged (len %d), got len %d", len(turns), len(got))
	}
	for i := range turns {
		if got[i] != turns[i] {
			t.Errorf("turn %d changed: got %+v, want %+v", i, got[i], turns[i])
		}
	}
}

// TestSummarizeOldTurns_OverThresholdCollapses confirms a chat with more
// than summarizeHistoryThreshold turns gets summarized down to
// summarizeHistoryThreshold+1 turns (one synthetic summary turn, then the
// most recent summarizeHistoryThreshold turns verbatim) before being sent to
// Generate.
func TestSummarizeOldTurns_OverThresholdCollapses(t *testing.T) {
	fg := &summarizingFakeGenerator{}
	total := summarizeHistoryThreshold + 15
	turns := turnsOf(total)

	got := summarizeOldTurns(context.Background(), fg, turns)

	if fg.summarizeCalls != 1 {
		t.Fatalf("expected exactly 1 Summarize call, got %d", fg.summarizeCalls)
	}
	if len(got) != summarizeHistoryThreshold+1 {
		t.Fatalf("expected %d turns after summarization, got %d", summarizeHistoryThreshold+1, len(got))
	}
	if got[0].Role != "user" {
		t.Errorf("expected synthetic summary turn to have role 'user', got %q", got[0].Role)
	}
	wantOlder := total - summarizeHistoryThreshold
	wantSummary := fmt.Sprintf("summary of %d turns", wantOlder)
	if !strings.Contains(got[0].Content, wantSummary) {
		t.Errorf("expected summary turn to contain %q, got %q", wantSummary, got[0].Content)
	}
	// The recent summarizeHistoryThreshold turns must be preserved verbatim,
	// in order, after the synthetic summary turn.
	recentWant := turns[total-summarizeHistoryThreshold:]
	for i, want := range recentWant {
		if got[i+1] != want {
			t.Errorf("recent turn %d: got %+v, want %+v", i, got[i+1], want)
		}
	}
}

// TestSummarizeOldTurns_FakeModeNeverCallsRealAPI confirms fake mode's
// Summarize (see ai.Generator.Summarize's fake branch) is deterministic and
// never errors — exercised here through the real *ai.Generator built via
// ai.NewFake, not a test double, since the requirement is specifically about
// fake mode's own behavior.
func TestSummarizeOldTurns_FakeModeNeverCallsRealAPI(t *testing.T) {
	fake := ai.NewFake(0)
	total := summarizeHistoryThreshold + 5
	turns := turnsOf(total)

	got := summarizeOldTurns(context.Background(), fake, turns)

	if len(got) != summarizeHistoryThreshold+1 {
		t.Fatalf("expected %d turns after summarization, got %d", summarizeHistoryThreshold+1, len(got))
	}
	wantOlder := total - summarizeHistoryThreshold
	wantSummary := fmt.Sprintf("[fake mode summary of %d turns]", wantOlder)
	if !strings.Contains(got[0].Content, wantSummary) {
		t.Errorf("expected fake summary turn to contain %q, got %q", wantSummary, got[0].Content)
	}
}

// TestSummarizeOldTurns_FailsOpenOnSummarizeError confirms a Summarize
// failure falls back to the full, unsummarized history rather than
// propagating an error — this is a cost optimization, never a new way for
// generation to break.
func TestSummarizeOldTurns_FailsOpenOnSummarizeError(t *testing.T) {
	fg := &summarizingFakeGenerator{summarizeErr: errors.New("boom")}
	turns := turnsOf(summarizeHistoryThreshold + 10)

	got := summarizeOldTurns(context.Background(), fg, turns)

	if fg.summarizeCalls != 1 {
		t.Fatalf("expected exactly 1 Summarize attempt, got %d", fg.summarizeCalls)
	}
	if len(got) != len(turns) {
		t.Fatalf("expected full unsummarized history (len %d) on Summarize failure, got len %d", len(turns), len(got))
	}
	for i := range turns {
		if got[i] != turns[i] {
			t.Errorf("turn %d changed on fallback: got %+v, want %+v", i, got[i], turns[i])
		}
	}
}


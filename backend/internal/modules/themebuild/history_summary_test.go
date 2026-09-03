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

// newCachedTestService builds a Service around fg with history summarization
// caching fully wired (matching what NewService sets up) — the bare
// &Service{gen: fg} literal used above is deliberately minimal for testing
// summarizeOldTurns in isolation, but summarizeOldTurnsCached needs its
// cache/lock fields non-nil to exercise the real caching path rather than
// its nil-guard fallback.
func newCachedTestService(fg generator, enabled bool) *Service {
	return &Service{
		gen:                         fg,
		historySummarizationEnabled: enabled,
		historySummaries:            newHistorySummaryCache(),
		historySummaryLocks:         newKeyedMutex(),
	}
}

// TestSummarizeOldTurnsCached_ExactlyAtThresholdUnchanged confirms the
// caching wrapper preserves summarizeOldTurns' own under/at-threshold
// behavior exactly — a chat at exactly summarizeHistoryThreshold turns must
// never summarize, cached or not.
func TestSummarizeOldTurnsCached_ExactlyAtThresholdUnchanged(t *testing.T) {
	fg := &summarizingFakeGenerator{}
	svc := newCachedTestService(fg, true)
	turns := turnsOf(summarizeHistoryThreshold)

	got := svc.summarizeOldTurnsCached(context.Background(), "chat-threshold", turns)

	if fg.summarizeCalls != 0 {
		t.Fatalf("expected no Summarize call at exactly the threshold, got %d", fg.summarizeCalls)
	}
	if len(got) != len(turns) {
		t.Fatalf("expected turns unchanged at exactly the threshold, got len %d want %d", len(got), len(turns))
	}
}

// TestSummarizeOldTurnsCached_SameChatOneSummarizeCall covers the core fix:
// two generations on the same chat, same older-turn set, must produce
// exactly one Summarize call — the second reuses the cached summary.
func TestSummarizeOldTurnsCached_SameChatOneSummarizeCall(t *testing.T) {
	fg := &summarizingFakeGenerator{}
	svc := newCachedTestService(fg, true)
	turns := turnsOf(summarizeHistoryThreshold + 5)

	first := svc.summarizeOldTurnsCached(context.Background(), "chat-1", turns)
	second := svc.summarizeOldTurnsCached(context.Background(), "chat-1", turns)

	if fg.summarizeCalls != 1 {
		t.Fatalf("expected exactly 1 Summarize call across two generations on the same chat, got %d", fg.summarizeCalls)
	}
	if first[0].Content != second[0].Content {
		t.Errorf("expected the second call's summary turn to reuse the cached content, got %q vs %q", first[0].Content, second[0].Content)
	}
}

// TestSummarizeOldTurnsCached_RecentChurnKeepsCacheHit confirms a cache hit
// is keyed on the older-turn set alone — a different set of RECENT turns
// (the part that's always resent verbatim, never summarized) attached to
// the exact same older prefix must still hit the cache, and the turns
// actually returned must be the current call's recent turns, never a stale
// copy from whichever call happened to populate the cache.
func TestSummarizeOldTurnsCached_RecentChurnKeepsCacheHit(t *testing.T) {
	fg := &summarizingFakeGenerator{}
	svc := newCachedTestService(fg, true)

	shared := turnsOf(5) // the shared 5-turn older prefix in a 25-turn chat
	recentA := make([]ai.Turn, summarizeHistoryThreshold)
	recentB := make([]ai.Turn, summarizeHistoryThreshold)
	for i := range recentA {
		recentA[i] = ai.Turn{Role: "user", Content: fmt.Sprintf("recent-A-%d", i)}
		recentB[i] = ai.Turn{Role: "user", Content: fmt.Sprintf("recent-B-%d", i)}
	}
	turnsA := append(append([]ai.Turn{}, shared...), recentA...)
	turnsB := append(append([]ai.Turn{}, shared...), recentB...)

	svc.summarizeOldTurnsCached(context.Background(), "chat-2", turnsA)
	got := svc.summarizeOldTurnsCached(context.Background(), "chat-2", turnsB)

	if fg.summarizeCalls != 1 {
		t.Fatalf("expected the second call (same older prefix, different recent tail) to hit the cache — exactly 1 Summarize call, got %d", fg.summarizeCalls)
	}
	if want := "recent-B-19"; got[len(got)-1].Content != want {
		t.Errorf("expected the cached call to still attach the CURRENT recent turns, got last turn content %q, want %q", got[len(got)-1].Content, want)
	}
}

// TestSummarizeOldTurnsCached_ChangedOlderSetRegenerates confirms a changed
// older-turn count — grown or shrunk (e.g. a discard/revert) — always
// misses the cache and regenerates, never reuses a summary that no longer
// covers the right turns.
func TestSummarizeOldTurnsCached_ChangedOlderSetRegenerates(t *testing.T) {
	fg := &summarizingFakeGenerator{}
	svc := newCachedTestService(fg, true)

	svc.summarizeOldTurnsCached(context.Background(), "chat-3", turnsOf(summarizeHistoryThreshold+5))
	if fg.summarizeCalls != 1 {
		t.Fatalf("expected the first call to summarize, got %d calls", fg.summarizeCalls)
	}

	svc.summarizeOldTurnsCached(context.Background(), "chat-3", turnsOf(summarizeHistoryThreshold+10))
	if fg.summarizeCalls != 2 {
		t.Fatalf("expected a grown older-turn set (5 -> 10 older turns) to miss the cache and regenerate, got %d Summarize calls", fg.summarizeCalls)
	}

	svc.summarizeOldTurnsCached(context.Background(), "chat-3", turnsOf(summarizeHistoryThreshold+3))
	if fg.summarizeCalls != 3 {
		t.Fatalf("expected a shrunk older-turn set (10 -> 3 older turns, e.g. after a revert) to also miss the cache and regenerate, got %d Summarize calls", fg.summarizeCalls)
	}
}

// TestSummarizeOldTurnsCached_ErrorNotCached confirms a Summarize failure
// falls back to full history without poisoning the cache — the next call on
// the same chat must retry Summarize rather than reusing a fallback.
func TestSummarizeOldTurnsCached_ErrorNotCached(t *testing.T) {
	fg := &summarizingFakeGenerator{summarizeErr: errors.New("boom")}
	svc := newCachedTestService(fg, true)
	turns := turnsOf(summarizeHistoryThreshold + 5)

	first := svc.summarizeOldTurnsCached(context.Background(), "chat-4", turns)
	if len(first) != len(turns) {
		t.Fatalf("expected full unsummarized history on Summarize failure, got len %d want %d", len(first), len(turns))
	}
	if fg.summarizeCalls != 1 {
		t.Fatalf("expected exactly 1 Summarize attempt, got %d", fg.summarizeCalls)
	}

	fg.summarizeErr = nil // the underlying failure was transient
	second := svc.summarizeOldTurnsCached(context.Background(), "chat-4", turns)
	if fg.summarizeCalls != 2 {
		t.Fatalf("expected the second call on the same chat to retry Summarize (a failure must never be cached), got %d total calls", fg.summarizeCalls)
	}
	if len(second) != summarizeHistoryThreshold+1 {
		t.Fatalf("expected the retry to succeed and collapse history, got len %d", len(second))
	}
}

// TestSummarizeOldTurnsCached_DisabledNeverSummarizes confirms
// HistorySummarizationEnabled=false behaves exactly like the under-
// threshold path — full history, unchanged, no Summarize call ever.
func TestSummarizeOldTurnsCached_DisabledNeverSummarizes(t *testing.T) {
	fg := &summarizingFakeGenerator{}
	svc := newCachedTestService(fg, false)
	turns := turnsOf(summarizeHistoryThreshold + 5)

	got := svc.summarizeOldTurnsCached(context.Background(), "chat-5", turns)

	if fg.summarizeCalls != 0 {
		t.Fatalf("expected Summarize never called when disabled, got %d calls", fg.summarizeCalls)
	}
	if len(got) != len(turns) {
		t.Fatalf("expected full unsummarized history when disabled, got len %d want %d", len(got), len(turns))
	}
}

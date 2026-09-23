package themebuild

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ai-chat/internal/ai"
)

// summarizingFakeGenerator has a Summarize call independently controllable from fakeGenerator's
// Generate — used to test summarizeOldTurns in isolation.
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

// A chat at or under summarizeHistoryThreshold turns is left completely alone — no Summarize call.
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

// A chat over summarizeHistoryThreshold turns collapses to threshold+1 turns: one summary turn
// plus the most recent threshold turns verbatim.
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
	// The recent summarizeHistoryThreshold turns must be preserved verbatim, in order.
	recentWant := turns[total-summarizeHistoryThreshold:]
	for i, want := range recentWant {
		if got[i+1] != want {
			t.Errorf("recent turn %d: got %+v, want %+v", i, got[i+1], want)
		}
	}
}

// Confirms fake mode's Summarize is deterministic and never errors, exercised through the real
// *ai.Generator built via ai.NewFake, not a test double.
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

// A Summarize failure falls back to full unsummarized history rather than propagating an error.
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

// newCachedTestService wires cache/lock fields non-nil (matching NewService) so
// summarizeOldTurnsCached exercises the real caching path, not its nil-guard fallback.
func newCachedTestService(fg generator, enabled bool) *Service {
	return &Service{
		gen:                         fg,
		historySummarizationEnabled: enabled,
		historySummaries:            newHistorySummaryCache(),
		historySummaryLocks:         newStripedMutex(historySummaryLockStripes),
	}
}

// A chat at exactly summarizeHistoryThreshold turns must never summarize, cached or not.
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

// Two generations on the same chat, same older-turn set, must produce exactly one Summarize call.
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

// A cache hit is keyed on the older-turn set alone; different RECENT turns on the same older
// prefix must still hit, returning the current call's own recent turns, not a stale copy.
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

// A changed older-turn count (grown or shrunk via discard/revert) always misses the cache and regenerates.
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

// A Summarize failure falls back to full history without poisoning the cache — the next call must retry.
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

// HistorySummarizationEnabled=false behaves like the under-threshold path: full history, no Summarize call.
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

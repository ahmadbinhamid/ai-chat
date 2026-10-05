package themebuild

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
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
	turns := turnsOf(summarizeHistoryThreshold + summaryBlockSize)

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

	shared := turnsOf(summaryBlockSize) // the shared one-block older prefix
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

// A changed older-turn bucket (grown or shrunk via discard/revert) always misses the cache and regenerates.
func TestSummarizeOldTurnsCached_ChangedOlderSetRegenerates(t *testing.T) {
	fg := &summarizingFakeGenerator{}
	svc := newCachedTestService(fg, true)

	svc.summarizeOldTurnsCached(context.Background(), "chat-3", turnsOf(summarizeHistoryThreshold+10))
	if fg.summarizeCalls != 1 {
		t.Fatalf("expected the first call to summarize, got %d calls", fg.summarizeCalls)
	}

	svc.summarizeOldTurnsCached(context.Background(), "chat-3", turnsOf(summarizeHistoryThreshold+20))
	if fg.summarizeCalls != 2 {
		t.Fatalf("expected a grown older-turn set (10 -> 20 older turns) to miss the cache and regenerate, got %d Summarize calls", fg.summarizeCalls)
	}

	svc.summarizeOldTurnsCached(context.Background(), "chat-3", turnsOf(summarizeHistoryThreshold+13))
	if fg.summarizeCalls != 3 {
		t.Fatalf("expected a shrunk older-turn set (20 -> 10 older turns, e.g. after a revert) to also miss the cache and regenerate, got %d Summarize calls", fg.summarizeCalls)
	}
}

// A Summarize failure falls back to full history without poisoning the cache — the next call must retry.
func TestSummarizeOldTurnsCached_ErrorNotCached(t *testing.T) {
	fg := &summarizingFakeGenerator{summarizeErr: errors.New("boom")}
	svc := newCachedTestService(fg, true)
	turns := turnsOf(summarizeHistoryThreshold + summaryBlockSize)

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

func TestBucketedOlderCount(t *testing.T) {
	tests := []struct {
		turns int
		want  int
	}{
		{0, 0},
		{summarizeHistoryThreshold, 0},
		{summarizeHistoryThreshold + 1, 0},
		{summarizeHistoryThreshold + summaryBlockSize - 1, 0},
		{summarizeHistoryThreshold + summaryBlockSize, summaryBlockSize},
		{summarizeHistoryThreshold + 2*summaryBlockSize - 1, summaryBlockSize},
		{summarizeHistoryThreshold + 2*summaryBlockSize, 2 * summaryBlockSize},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d turns", tt.turns), func(t *testing.T) {
			if got := bucketedOlderCount(tt.turns); got != tt.want {
				t.Errorf("bucketedOlderCount(%d) = %d, want %d", tt.turns, got, tt.want)
			}
		})
	}
}

// Consecutive turns past the threshold within one block must reuse one summary, byte-identical.
func TestSummarizeOldTurnsCached_ConsecutiveTurnsOneSummarizeCall(t *testing.T) {
	fg := &summarizingFakeGenerator{}
	svc := newCachedTestService(fg, true)
	all := turnsOf(summarizeHistoryThreshold + 2*summaryBlockSize)

	var first string
	for n := summarizeHistoryThreshold + summaryBlockSize; n < summarizeHistoryThreshold+2*summaryBlockSize; n++ {
		got := svc.summarizeOldTurnsCached(context.Background(), "chat-block", all[:n])
		if n == summarizeHistoryThreshold+summaryBlockSize {
			first = got[0].Content
		} else if got[0].Content != first {
			t.Fatalf("turn count %d: summary turn changed within a block: %q vs %q", n, got[0].Content, first)
		}
		if want := n - summaryBlockSize + 1; len(got) != want {
			t.Fatalf("turn count %d: got %d turns, want %d", n, len(got), want)
		}
		if got[len(got)-1] != all[n-1] {
			t.Fatalf("turn count %d: latest turn not preserved", n)
		}
	}
	if fg.summarizeCalls != 1 {
		t.Fatalf("expected 1 Summarize call across a whole block of turns, got %d", fg.summarizeCalls)
	}
	if want := fmt.Sprintf("summary of %d turns", summaryBlockSize); !strings.Contains(first, want) {
		t.Errorf("expected summary of exactly the bucketed older turns (%q), got %q", want, first)
	}

	got := svc.summarizeOldTurnsCached(context.Background(), "chat-block", all)
	if fg.summarizeCalls != 2 {
		t.Fatalf("expected crossing a block boundary to trigger exactly one new Summarize, got %d total", fg.summarizeCalls)
	}
	if want := fmt.Sprintf("%d turns condensed", 2*summaryBlockSize); !strings.Contains(got[0].Content, want) {
		t.Errorf("expected new summary turn to use the bucketed count (%q), got %q", want, got[0].Content)
	}
}

// Past the threshold but short of one full block, nothing is summarized yet.
func TestSummarizeOldTurnsCached_PartialFirstBlockUnchanged(t *testing.T) {
	fg := &summarizingFakeGenerator{}
	svc := newCachedTestService(fg, true)
	turns := turnsOf(summarizeHistoryThreshold + summaryBlockSize - 1)

	got := svc.summarizeOldTurnsCached(context.Background(), "chat-partial", turns)

	if fg.summarizeCalls != 0 {
		t.Fatalf("expected no Summarize call before the first full block, got %d", fg.summarizeCalls)
	}
	if len(got) != len(turns) {
		t.Fatalf("expected full history, got len %d want %d", len(got), len(turns))
	}
}

// A shrink back across a block boundary summarizes the lower block's exact prefix.
func TestSummarizeOldTurnsCached_ShrinkAcrossBoundary(t *testing.T) {
	fg := &summarizingFakeGenerator{}
	svc := newCachedTestService(fg, true)
	all := turnsOf(summarizeHistoryThreshold + 2*summaryBlockSize)
	svc.summarizeOldTurnsCached(context.Background(), "chat-shrink", all)

	n := summarizeHistoryThreshold + summaryBlockSize + 3
	got := svc.summarizeOldTurnsCached(context.Background(), "chat-shrink", all[:n])

	if fg.summarizeCalls != 2 {
		t.Fatalf("expected the shrink to miss and re-summarize, got %d calls", fg.summarizeCalls)
	}
	if want := fmt.Sprintf("summary of %d turns", summaryBlockSize); !strings.Contains(got[0].Content, want) {
		t.Errorf("expected summary of the lower block (%q), got %q", want, got[0].Content)
	}
	if len(got) != 1+n-summaryBlockSize || got[1] != all[summaryBlockSize] {
		t.Errorf("expected recent window to start right after the lower block")
	}
}

// An evicted entry just re-summarizes to the same content.
func TestSummarizeOldTurnsCached_EvictedEntryResummarizes(t *testing.T) {
	fg := &summarizingFakeGenerator{}
	svc := newCachedTestService(fg, true)
	turns := turnsOf(summarizeHistoryThreshold + summaryBlockSize)

	first := svc.summarizeOldTurnsCached(context.Background(), "chat-evict", turns)
	svc.historySummaries.mu.Lock()
	delete(svc.historySummaries.entries, "chat-evict")
	svc.historySummaries.mu.Unlock()
	second := svc.summarizeOldTurnsCached(context.Background(), "chat-evict", turns)

	if fg.summarizeCalls != 2 {
		t.Fatalf("expected an evicted entry to re-summarize, got %d calls", fg.summarizeCalls)
	}
	if first[0].Content != second[0].Content {
		t.Errorf("expected identical summary turn after eviction, got %q vs %q", first[0].Content, second[0].Content)
	}
}

// A Summarize error on the bucketed path falls back to full history and isn't cached.
func TestSummarizeOldTurnsCached_BucketedErrorFailsOpen(t *testing.T) {
	fg := &summarizingFakeGenerator{summarizeErr: errors.New("boom")}
	svc := newCachedTestService(fg, true)
	turns := turnsOf(summarizeHistoryThreshold + summaryBlockSize + 4)

	got := svc.summarizeOldTurnsCached(context.Background(), "chat-err", turns)
	if len(got) != len(turns) {
		t.Fatalf("expected full history on error, got len %d want %d", len(got), len(turns))
	}
	if _, ok := svc.historySummaries.get("chat-err", summaryBlockSize); ok {
		t.Fatalf("expected a failed Summarize never to be cached")
	}
}

// The summary cache relies on replayed history being append-only: a discard/revert only stamps
// apply_status, so the turn must still be replayed, or the older-turn set would shift under a cached summary.
func TestToTurns_DiscardedMessagesStillReplayed(t *testing.T) {
	msgs := []chat.Message{
		{Role: chat.RoleUser, Content: "make it blue", ApplyStatus: chat.ApplyStatusDiscarded},
		{Role: chat.RoleAssistant, Content: "Made it blue.", Status: chat.MessageStatusCompleted, ApplyStatus: chat.ApplyStatusDiscarded},
	}
	if got := toTurns(msgs); len(got) != 2 {
		t.Fatalf("expected discarded messages still replayed, got %d turns", len(got))
	}
}

// recordingSummarizer records every Summarize input so tests can assert on exactly what was sent.
type recordingSummarizer struct {
	fakeGenerator
	inputs [][]ai.Turn
}

func (r *recordingSummarizer) Summarize(_ context.Context, turns []ai.Turn) (string, error) {
	r.inputs = append(r.inputs, append([]ai.Turn(nil), turns...))
	return fmt.Sprintf("summary of %d turns", len(turns)), nil
}

// bigTurnsOf returns n turns, each turnChars long and tagged with its index so position is checkable.
func bigTurnsOf(n, turnChars int) []ai.Turn {
	turns := make([]ai.Turn, n)
	for i := range turns {
		tag := fmt.Sprintf("turn-%04d:", i)
		turns[i] = ai.Turn{Role: "user", Content: tag + strings.Repeat("x", turnChars-len(tag))}
	}
	return turns
}

func inputChars(turns []ai.Turn) int {
	total := 0
	for _, t := range turns {
		total += len(t.Content)
	}
	return total
}

func TestSummaryWindow(t *testing.T) {
	tests := []struct {
		name      string
		turns     []ai.Turn
		wantCount int
	}{
		{"empty", nil, 0},
		{"under budget keeps all", bigTurnsOf(30, 1_000), 30},
		{"exactly at budget keeps all", bigTurnsOf(20, summarizeMaxInputChars/20), 20},
		{"over budget keeps newest that fit", bigTurnsOf(1_000, 1_000), summarizeMaxInputChars / 1_000},
		{"single oversized turn is still kept", bigTurnsOf(3, summarizeMaxInputChars+1), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summaryWindow(tt.turns)
			if len(got) != tt.wantCount {
				t.Fatalf("expected %d turns, got %d", tt.wantCount, len(got))
			}
			if len(got) > 0 && got[len(got)-1] != tt.turns[len(tt.turns)-1] {
				t.Errorf("expected the window to end at the newest older turn")
			}
		})
	}
}

// A long chat must send Summarize at most summarizeMaxInputChars, keeping the newest older turns and reporting the real count.
func TestSummarizeOldTurnsCached_CapsSummarizeInput(t *testing.T) {
	rec := &recordingSummarizer{}
	svc := newCachedTestService(rec, true)
	turns := bigTurnsOf(1_000, 2_000)
	olderCount := bucketedOlderCount(len(turns))

	got := svc.summarizeOldTurnsCached(context.Background(), "chat-cap", turns)

	if len(rec.inputs) != 1 {
		t.Fatalf("expected 1 Summarize call, got %d", len(rec.inputs))
	}
	input := rec.inputs[0]
	if n := inputChars(input); n > summarizeMaxInputChars {
		t.Errorf("Summarize input is %d chars, over the %d budget", n, summarizeMaxInputChars)
	}
	if input[len(input)-1] != turns[olderCount-1] {
		t.Errorf("expected the newest older turn to be kept, got last input turn %q", input[len(input)-1].Content[:10])
	}
	if input[0] != turns[olderCount-len(input)] || input[0] == turns[0] {
		t.Errorf("expected the oldest turns to be dropped, got first input turn %q", input[0].Content[:10])
	}
	want := fmt.Sprintf("%d turns condensed]: summary of %d turns", len(input), len(input))
	if !strings.Contains(got[0].Content, want) {
		t.Errorf("expected summary turn to report the summarized count %q, got %q", want, got[0].Content)
	}
	if len(got) != 1+len(turns)-olderCount {
		t.Errorf("expected recent turns untouched: got %d turns, want %d", len(got), 1+len(turns)-olderCount)
	}

	again := svc.summarizeOldTurnsCached(context.Background(), "chat-cap", turns)
	if len(rec.inputs) != 1 || again[0].Content != got[0].Content {
		t.Errorf("expected a cache hit with an identical summary turn, got %d calls, %q vs %q", len(rec.inputs), again[0].Content, got[0].Content)
	}
}

// Every turn count within one block must produce a byte-identical Summarize input, even past the cap.
func TestSummarizeOldTurnsCached_CappedInputStableWithinBlock(t *testing.T) {
	rec := &recordingSummarizer{}
	svc := &Service{gen: rec, historySummarizationEnabled: true}
	all := bigTurnsOf(1_000+summaryBlockSize, 2_000)

	base := 1_000 - (1_000-summarizeHistoryThreshold)%summaryBlockSize
	for n := base; n < base+summaryBlockSize; n++ {
		svc.summarizeOldTurnsCached(context.Background(), "chat-stable", all[:n])
	}

	if len(rec.inputs) != summaryBlockSize {
		t.Fatalf("expected %d uncached Summarize calls, got %d", summaryBlockSize, len(rec.inputs))
	}
	first := fmt.Sprintf("%v", rec.inputs[0])
	for i, in := range rec.inputs[1:] {
		if fmt.Sprintf("%v", in) != first {
			t.Errorf("call %d in the same block sent a different Summarize input", i+1)
		}
	}
}

// The uncached helper must apply the same cap as the production path.
func TestSummarizeOldTurns_CapsSummarizeInput(t *testing.T) {
	rec := &recordingSummarizer{}
	turns := bigTurnsOf(1_000, 2_000)

	got := summarizeOldTurns(context.Background(), rec, turns)

	if len(rec.inputs) != 1 {
		t.Fatalf("expected 1 Summarize call, got %d", len(rec.inputs))
	}
	input := rec.inputs[0]
	if n := inputChars(input); n > summarizeMaxInputChars {
		t.Errorf("Summarize input is %d chars, over the %d budget", n, summarizeMaxInputChars)
	}
	if input[len(input)-1] != turns[len(turns)-summarizeHistoryThreshold-1] {
		t.Errorf("expected the newest older turn to be kept")
	}
	if want := fmt.Sprintf("%d turns condensed]", len(input)); !strings.Contains(got[0].Content, want) {
		t.Errorf("expected %q in summary turn, got %q", want, got[0].Content)
	}
}

// Below the cap, both paths still summarize every older turn.
func TestSummarizeOldTurnsCached_BelowCapUnchanged(t *testing.T) {
	rec := &recordingSummarizer{}
	svc := newCachedTestService(rec, true)
	turns := turnsOf(summarizeHistoryThreshold + 3*summaryBlockSize)
	olderCount := bucketedOlderCount(len(turns))

	got := svc.summarizeOldTurnsCached(context.Background(), "chat-small", turns)

	if len(rec.inputs) != 1 || len(rec.inputs[0]) != olderCount {
		t.Fatalf("expected all %d older turns summarized, got %+v", olderCount, rec.inputs)
	}
	if want := summaryTurnContent(olderCount, fmt.Sprintf("summary of %d turns", olderCount)); got[0].Content != want {
		t.Errorf("expected unchanged summary turn %q, got %q", want, got[0].Content)
	}
}

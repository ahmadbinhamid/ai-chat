package themebuild

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"ai-chat/internal/ai"
)

// Beyond this turn count, older turns collapse into one summary turn.
const summarizeHistoryThreshold = 20

// Older-turn count is floored to a multiple of this so the cache key and summary text hold for a whole block;
// a rolling key re-summarizes every turn and shifts the prefix, defeating both this cache and DeepSeek prefix matching.
const summaryBlockSize = 10

// Recent window floats between summarizeHistoryThreshold and threshold+summaryBlockSize-1 turns; 0 means don't summarize.
func bucketedOlderCount(turnCount int) int {
	older := turnCount - summarizeHistoryThreshold
	if older <= 0 {
		return 0
	}
	return older - older%summaryBlockSize
}

// Character budget for one Summarize input. Sized against DeepSeek's smallest shipped window (128K tokens): theme
// turns are HTML/Liquid-heavy at ~3 chars/token, so 200K chars is ~67K tokens, leaving room for the instruction and output.
const summarizeMaxInputChars = 200_000

// summaryWindow keeps the newest older turns that fit summarizeMaxInputChars, always at least one. Depends only on
// older, which is fixed within a summaryBlockSize block, so the Summarize input stays byte-identical across the block.
func summaryWindow(older []ai.Turn) []ai.Turn {
	total := 0
	start := len(older)
	for start > 0 {
		size := len(older[start-1].Content)
		if total+size > summarizeMaxInputChars && start < len(older) {
			break
		}
		total += size
		start--
	}
	return older[start:]
}

func logSummaryCap(chatID string, olderCount, summarizedCount int) {
	if summarizedCount < olderCount {
		slog.Info("ai: history summary input capped", "chat_id", chatID, "older_turn_count", olderCount,
			"summarized_turn_count", summarizedCount, "max_input_chars", summarizeMaxInputChars)
	}
}

// Shared by cached/uncached paths for byte-identical content (required for prefix-caching hit).
func summaryTurnContent(olderCount int, summary string) string {
	return "[Earlier conversation summary, " + strconv.Itoa(olderCount) + " turns condensed]: " + summary
}

func summaryTurn(olderCount int, summary string) ai.Turn {
	return ai.Turn{Role: "user", Content: summaryTurnContent(olderCount, summary)}
}

// Core for both summarizeOldTurns and summarizeOldTurnsCached paths.
func summarizeOlderTurns(ctx context.Context, gen generator, turns []ai.Turn) (summary string, olderCount int, err error) {
	older := turns[:len(turns)-summarizeHistoryThreshold]
	window := summaryWindow(older)
	logSummaryCap("", len(older), len(window))
	summary, err = gen.Summarize(ctx, "", window)
	return summary, len(window), err
}

// Uncached; fails open (returns full history on Summarize error).
func summarizeOldTurns(ctx context.Context, gen generator, turns []ai.Turn) []ai.Turn {
	if len(turns) <= summarizeHistoryThreshold {
		return turns
	}

	recent := turns[len(turns)-summarizeHistoryThreshold:]
	summary, olderCount, err := summarizeOlderTurns(ctx, gen, turns)
	if err != nil {
		slog.Warn("history summarization failed; falling back to full unsummarized history",
			"turn_count", len(turns), "older_turn_count", olderCount, "error", err)
		return turns
	}

	return append([]ai.Turn{summaryTurn(olderCount, summary)}, recent...)
}

// Eviction arbitrary (not LRU); wrong eviction costs only one extra Summarize call.
const historySummaryCacheMaxEntries = 2048

// Lock held across full Summarize call; 256 keeps collisions rare.
const historySummaryLockStripes = 256

// Lookup matches both chat ID and olderTurnCount; changed set misses and regenerates.
type historySummaryCacheEntry struct {
	olderTurnCount int
	summary        string
}

// Never stale: replayed history is append-only (revert/discard only update apply_status, which toTurns ignores).
type historySummaryCache struct {
	mu      sync.Mutex
	entries map[string]historySummaryCacheEntry
}

func newHistorySummaryCache() *historySummaryCache {
	return &historySummaryCache{entries: make(map[string]historySummaryCacheEntry)}
}

func (c *historySummaryCache) get(chatID string, olderTurnCount int) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[chatID]
	if !ok || entry.olderTurnCount != olderTurnCount {
		return "", false
	}
	return entry.summary, true
}

func (c *historySummaryCache) set(chatID string, olderTurnCount int, summary string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[chatID]; !exists && len(c.entries) >= historySummaryCacheMaxEntries {
		// Evict arbitrary entry (randomized map iteration).
		for k := range c.entries {
			delete(c.entries, k)
			break
		}
	}
	c.entries[chatID] = historySummaryCacheEntry{olderTurnCount: olderTurnCount, summary: summary}
}

// summarizeOldTurnsCached is summarizeOldTurns plus a per-chat cache — what doGenerate actually
// calls. Matters because DeepSeek caches on exact request-prefix match, so a regenerated summary would defeat that cache every call. Fails open and is never cached, so a transient error can't poison it.
func (s *Service) summarizeOldTurnsCached(ctx context.Context, chatID string, turns []ai.Turn) []ai.Turn {
	if !s.historySummarizationEnabled {
		return turns
	}
	// Computed once from turns, so the key checked inside and outside the lock is always the same.
	olderCount := bucketedOlderCount(len(turns))
	if olderCount == 0 {
		return turns
	}
	recent := turns[olderCount:]
	window := summaryWindow(turns[:olderCount])

	if s.historySummaries != nil {
		if summary, ok := s.historySummaries.get(chatID, olderCount); ok {
			slog.Info("ai: history summarization", "chat_id", chatID, "ran", false, "cache_hit", true, "older_turn_count", olderCount)
			return append([]ai.Turn{summaryTurn(len(window), summary)}, recent...)
		}
	}

	// Nil only in bare-Service tests; skipping the lock then risks at most a benign duplicate
	// Summarize call, never a race (historySummaryCache is concurrency-safe on its own).
	if s.historySummaryLocks != nil {
		// Error discarded safely: this is the concrete *stripedMutex (always nil error), not themeLocker.
		unlock, _ := s.historySummaryLocks.Lock(ctx, chatID)
		defer unlock()

		if s.historySummaries != nil {
			if summary, ok := s.historySummaries.get(chatID, olderCount); ok {
				slog.Info("ai: history summarization", "chat_id", chatID, "ran", false, "cache_hit", true, "older_turn_count", olderCount)
				return append([]ai.Turn{summaryTurn(len(window), summary)}, recent...)
			}
		}
	}

	logSummaryCap(chatID, olderCount, len(window))
	start := time.Now()
	summary, err := s.gen.Summarize(ctx, chatID, window)
	elapsed := time.Since(start)
	if err != nil {
		slog.Warn("history summarization failed; falling back to full unsummarized history",
			"turn_count", len(turns), "older_turn_count", olderCount, "error", err)
		slog.Info("ai: history summarization", "chat_id", chatID, "ran", true, "cache_hit", false,
			"older_turn_count", olderCount, "elapsed_ms", elapsed.Milliseconds(), "error", true)
		return turns
	}

	if s.historySummaries != nil {
		s.historySummaries.set(chatID, olderCount, summary)
	}
	slog.Info("ai: history summarization", "chat_id", chatID, "ran", true, "cache_hit", false,
		"older_turn_count", olderCount, "elapsed_ms", elapsed.Milliseconds(), "error", false)
	return append([]ai.Turn{summaryTurn(len(window), summary)}, recent...)
}

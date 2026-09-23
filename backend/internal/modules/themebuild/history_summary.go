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
	summary, err = gen.Summarize(ctx, older)
	return summary, len(older), err
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

// In-process best-effort cache; fixed older-turn sets never go stale (size-capped map enough).
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
	if !s.historySummarizationEnabled || len(turns) <= summarizeHistoryThreshold {
		return turns
	}

	recent := turns[len(turns)-summarizeHistoryThreshold:]
	olderCount := len(turns) - summarizeHistoryThreshold

	if s.historySummaries != nil {
		if summary, ok := s.historySummaries.get(chatID, olderCount); ok {
			slog.Info("ai: history summarization", "chat_id", chatID, "ran", false, "cache_hit", true, "older_turn_count", olderCount)
			return append([]ai.Turn{summaryTurn(olderCount, summary)}, recent...)
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
				return append([]ai.Turn{summaryTurn(olderCount, summary)}, recent...)
			}
		}
	}

	start := time.Now()
	summary, _, err := summarizeOlderTurns(ctx, s.gen, turns)
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
	return append([]ai.Turn{summaryTurn(olderCount, summary)}, recent...)
}

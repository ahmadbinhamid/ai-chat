package themebuild

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"ai-chat/internal/ai"
)

// summarizeHistoryThreshold is the turn count (after toTurns' empty-turn
// filtering) beyond which older turns get collapsed into one synthetic
// summary turn instead of being resent verbatim on every Claude call — see
// summarizeOldTurns. Chosen so a chat has to genuinely run long before this
// kicks in; most chats never hit it.
const summarizeHistoryThreshold = 20

// summaryTurnContent formats a collapsed-history summary turn's text —
// shared by summarizeOldTurns (always regenerates) and Service's cache-
// aware summarizeOldTurnsCached (may reuse a cached summary string instead
// of calling gen.Summarize again), so both produce byte-identical turn
// content regardless of which path computed it. That identity matters on a
// prefix-caching provider (see summarizeOldTurnsCached's doc comment): two
// different strings for the same older-turn set would be two different
// request prefixes.
func summaryTurnContent(olderCount int, summary string) string {
	return "[Earlier conversation summary, " + strconv.Itoa(olderCount) + " turns condensed]: " + summary
}

func summaryTurn(olderCount int, summary string) ai.Turn {
	return ai.Turn{Role: "user", Content: summaryTurnContent(olderCount, summary)}
}

// summarizeOlderTurns calls gen.Summarize on the turns beyond the most
// recent summarizeHistoryThreshold and reports how many turns it condensed
// — the shared core both summarizeOldTurns (below) and
// summarizeOldTurnsCached build their synthetic turn from, factored out so
// neither has to duplicate the split/call/report logic and so a cache hit
// can skip straight past it.
func summarizeOlderTurns(ctx context.Context, gen generator, turns []ai.Turn) (summary string, olderCount int, err error) {
	older := turns[:len(turns)-summarizeHistoryThreshold]
	summary, err = gen.Summarize(ctx, older)
	return summary, len(older), err
}

// summarizeOldTurns bounds how much of a chat's history is replayed to the
// model on every single call by collapsing everything beyond the most
// recent summarizeHistoryThreshold turns into one synthetic summary turn —
// see summarizeOlderTurns. This is the uncached primitive: every call here
// is a fresh gen.Summarize round-trip, which is what Service's
// summarizeOldTurnsCached exists to avoid on the hot path (see its own doc
// comment for why an uncached call here is actively counterproductive on a
// prefix-caching provider, not just wasteful). Kept as a standalone,
// directly-tested function — not deleted or folded into
// summarizeOldTurnsCached — because it's still the correct place to pin
// down the threshold behavior, the summary turn's exact format, and the
// fail-open contract in isolation from caching.
//
// If turns is at or under summarizeHistoryThreshold, it's returned
// unchanged. Otherwise the oldest turns beyond the most recent
// summarizeHistoryThreshold are collapsed into a single synthetic "user"
// turn — a prose summary from one cheap model call — prepended before the
// recent turns, so the final list sent to the real generation call is
// always at most summarizeHistoryThreshold+1 turns regardless of how long
// the chat actually is.
//
// Summarization failure fails open: this is a cost optimization, not a
// correctness requirement, so an error here must never break generation
// itself. On error, it's logged and the full, unsummarized history is
// returned instead — the caller pays the verbose-history cost for this one
// call rather than losing the turn entirely.
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

// historySummaryCacheMaxEntries bounds historySummaryCache's total size —
// one entry per chat that has ever crossed summarizeHistoryThreshold,
// capped so a long-lived process that's served many distinct chats can't
// grow this map without bound. Eviction (see historySummaryCache.set) is
// arbitrary, not LRU: cheap to implement, and a wrongly-evicted entry only
// costs one avoidable Summarize call on that chat's next generation, never
// a correctness problem.
const historySummaryCacheMaxEntries = 2048

// historySummaryCacheEntry is what's cached per chat: the summary text plus
// how many older turns it covers. A lookup requires both the chat ID (the
// map key) and this count to match — see historySummaryCache.get — so a
// chat whose older-turn set has grown (a new turn crossed the threshold) or
// shrunk (a discard/revert) since the summary was computed misses and
// regenerates rather than reusing a now-stale summary.
type historySummaryCacheEntry struct {
	olderTurnCount int
	summary        string
}

// historySummaryCache is an in-process, best-effort cache of one collapsed-
// history summary per chat — see Service.summarizeOldTurnsCached. Unlike
// auth.MemoryCache (TTL-based: a cached token introspection genuinely goes
// stale) a summary of a fixed older-turn set never goes stale on its own,
// so there's nothing to expire here — a plain size-capped map behind a
// mutex is enough. Correctness never depends on a hit: a cold cache after a
// restart, or a miss on a different replica than the one that computed it,
// just costs one extra Summarize call, exactly like any other cache miss —
// never a wrong answer.
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
		// Evict one arbitrary entry — Go map iteration order is randomized,
		// so this is effectively a random eviction, not LRU. Good enough for
		// a best-effort cache; see the cache's own doc comment.
		for k := range c.entries {
			delete(c.entries, k)
			break
		}
	}
	c.entries[chatID] = historySummaryCacheEntry{olderTurnCount: olderTurnCount, summary: summary}
}

// summarizeOldTurnsCached is summarizeOldTurns plus a per-chat cache of the
// summary text — this, not summarizeOldTurns directly, is what doGenerate
// calls. Reusing a cached summary instead of regenerating it on every
// generation matters for two independent reasons:
//
//  1. Cost/latency: a summary of turns 1..N doesn't change when turn N+1
//     arrives, so regenerating it on every single call (the previous
//     behavior) was a full, blocking model round-trip paid over and over for
//     an answer that hadn't changed.
//  2. Provider prefix caching: DeepSeek's Anthropic-compat endpoint does not
//     honor cache_control — it caches on exact request-prefix match instead
//     (confirmed via cache_read_input_tokens in production logs). The old,
//     always-regenerated summary text differed on every call, which changed
//     the message prefix sent to the model on every call, which defeated
//     that prefix cache for the ENTIRE conversation, not just the summary
//     itself. A cache hit here returns the exact same summary text as last
//     time, keeping the prefix stable until the older-turn set actually
//     changes.
//
// A cache hit requires the chat ID AND the older-turn count to match (see
// historySummaryCache.get) — an entry is only reused when it covers exactly
// the same turns; a chat that has grown past a new threshold multiple, or
// shrunk via discard/revert, misses and regenerates. Concurrent generations
// on the same chat are serialized through historySummaryLocks (an
// in-process keyedMutex, not the distributed themeLocks — this cache is
// itself in-process only, so cross-replica locking would guard nothing) so
// two in-flight calls don't both pay for the same summary; the cache is
// re-checked after acquiring the lock in case a concurrent call already
// filled it while this one was waiting.
//
// Gated by s.historySummarizationEnabled (see
// config.Config.HistorySummarizationEnabled) — when disabled, turns is
// returned unchanged, same as the existing under-threshold path, and
// gen.Summarize is never called. A Summarize failure still fails open
// exactly as summarizeOldTurns does, and — critically — is never cached: a
// transient error must not poison the cache for the rest of the process's
// life, so the next call on this chat retries instead of reusing a fallback.
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

	// s.historySummaryLocks is nil only in tests that build a bare Service
	// around a fake generator (same nil-guard style as s.repo elsewhere in
	// this package) — skipping the lock in that case only risks a benign
	// duplicate Summarize call, never a crash or a race on the cache itself
	// (historySummaryCache is safe for concurrent use on its own).
	if s.historySummaryLocks != nil {
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

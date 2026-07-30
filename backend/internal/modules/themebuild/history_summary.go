package themebuild

import (
	"context"
	"log/slog"
	"strconv"

	"ai-chat/internal/ai"
)

// summarizeHistoryThreshold is the turn count (after toTurns' empty-turn
// filtering) beyond which older turns get collapsed into one synthetic
// summary turn instead of being resent verbatim on every Claude call — see
// summarizeOldTurns. Chosen so a chat has to genuinely run long before this
// kicks in; most chats never hit it.
const summarizeHistoryThreshold = 20

// summarizeOldTurns bounds how much of a chat's history is replayed to
// Claude on every single call. Every message sent to the model currently
// resends the ENTIRE prior conversation verbatim (mitigated only by
// Anthropic's own prompt caching on the last turn) — for a chat with many
// turns, that's unnecessary cost, since old turns rarely matter as much as
// the most recent ones for continuing the same thread of work.
//
// If turns is at or under summarizeHistoryThreshold, it's returned
// unchanged. Otherwise the oldest turns beyond the most recent
// summarizeHistoryThreshold are collapsed into a single synthetic "user"
// turn — a prose summary from one cheap Claude call (gen.Summarize) —
// prepended before the recent turns, so the final list sent to the real
// generation call is always at most summarizeHistoryThreshold+1 turns
// regardless of how long the chat actually is.
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

	older := turns[:len(turns)-summarizeHistoryThreshold]
	recent := turns[len(turns)-summarizeHistoryThreshold:]

	summary, err := gen.Summarize(ctx, older)
	if err != nil {
		slog.Warn("history summarization failed; falling back to full unsummarized history",
			"turn_count", len(turns), "older_turn_count", len(older), "error", err)
		return turns
	}

	summaryTurn := ai.Turn{
		Role:    "user",
		Content: "[Earlier conversation summary, " + strconv.Itoa(len(older)) + " turns condensed]: " + summary,
	}
	return append([]ai.Turn{summaryTurn}, recent...)
}

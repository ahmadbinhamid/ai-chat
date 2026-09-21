package buildercontext

import "ai-chat/internal/ai"

// SelectHistory applies HistoryPolicy to prior turns for a generation call.
// Does not mutate persisted chat history.
//
// When Focused+SkipSummarize: return at most MaxRecentTurns (or empty when 0).
// When not focused: return turns unchanged so the caller can summarize.
func SelectHistory(turns []ai.Turn, policy HistoryPolicy) (out []ai.Turn, historyMessages int, applied bool) {
	if !policy.Focused {
		return turns, len(turns), false
	}
	n := policy.MaxRecentTurns
	if n < 0 {
		n = MinHistoryFocused
	}
	if n == 0 {
		return nil, 0, true
	}
	if len(turns) <= n {
		return turns, len(turns), true
	}
	out = make([]ai.Turn, n)
	copy(out, turns[len(turns)-n:])
	return out, len(out), true
}

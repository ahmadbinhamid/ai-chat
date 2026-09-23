package themebuild

import (
	"strings"

	"ai-chat/internal/modules/chat"
)

// isReplayedMessage must stay in sync with toTurns; drift lets a carried-forward
// attachment outlive its summarized-away turn, or drops one still in the replayed window.
func isReplayedMessage(m chat.Message) bool {
	if strings.TrimSpace(m.Content) == "" {
		return false
	}
	switch m.Role {
	case chat.RoleUser:
		return true
	case chat.RoleAssistant:
		return m.Status == chat.MessageStatusCompleted
	default:
		return false
	}
}

// looksLikeFetchedLink reports whether filename is a URL rather than an uploaded
// file's name; there's no DB column recording this, so the URL prefix is the only signal.
func looksLikeFetchedLink(filename string) bool {
	return strings.HasPrefix(filename, "http://") || strings.HasPrefix(filename, "https://")
}

// findCarryForwardSourceMessageID finds the most recent earlier HTML-attached turn, bounded to
// the same replay window as history summarization. priorMessages must be oldest-first.
func findCarryForwardSourceMessageID(priorMessages []chat.Message, currentMessageID string) (string, bool) {
	replayed := make([]chat.Message, 0, len(priorMessages))
	for _, m := range priorMessages {
		if isReplayedMessage(m) {
			replayed = append(replayed, m)
		}
	}

	windowStart := 0
	if len(replayed) > summarizeHistoryThreshold {
		windowStart = len(replayed) - summarizeHistoryThreshold
	}

	for i := len(replayed) - 1; i >= windowStart; i-- {
		m := replayed[i]
		if m.ID == currentMessageID {
			continue
		}
		for _, a := range m.Attachments {
			if a.Kind == chat.AttachmentKindHTML {
				return m.ID, true
			}
		}
	}
	return "", false
}

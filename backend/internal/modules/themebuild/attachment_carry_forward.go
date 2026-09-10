package themebuild

import (
	"strings"

	"ai-chat/internal/modules/chat"
)

// isReplayedMessage reports whether m would produce an entry in toTurns'
// output — the single source of truth toTurns itself now delegates to (see
// its own doc comment), and what findCarryForwardSourceMessageID uses to
// determine whether an earlier turn is still inside the same window
// summarizeOldTurns/summarizeOldTurnsCached actually replay to the model.
// Any drift between the two would let a carried-forward attachment survive
// past the point its own turn has already been summarized away, or the
// reverse — silently dropping a reference that's still in the replayed
// window.
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

// looksLikeFetchedLink reports whether filename is a URL rather than an
// uploaded file's name. Once an HTML attachment is back from storage
// (chat_message_attachments has no separate "was this a link" column — see
// the 20260909000002 migration; adding one was rejected as an unnecessary
// schema change when the filename already carries this unambiguously), this
// is the only signal available: urlfetch.ValidateURL only ever accepts an
// http/https URL as the "filename" for a link-fetched attachment (see
// service.go's Generate, which stores the raw URL string as
// HTMLAttachmentFilename), while a genuine upload's filename comes from the
// merchant's own file picker and — barring a deliberately adversarial
// filename — never takes this shape.
func looksLikeFetchedLink(filename string) bool {
	return strings.HasPrefix(filename, "http://") || strings.HasPrefix(filename, "https://")
}

// findCarryForwardSourceMessageID returns the message ID of the most recent
// EARLIER user turn (excluding currentMessageID) that attached an HTML
// reference — an uploaded file or a fetched link — for doGenerate to resolve
// as the active reference when the current turn attached none of its own.
// Metadata only (priorMessages' Attachments carries no bytes — see
// chat.MessageAttachment's own doc comment), so this never touches
// chat_message_attachments' content column; the caller still has to fetch
// the bytes itself via GetAttachmentsContent once a source is found here.
//
// Bounded to the same window toTurns/summarizeOldTurnsCached actually
// replay to the model: a reference from a turn that's already been
// summarized away is gone for this turn too, exactly like the rest of that
// turn's content. Without this bound, a merchant could reference a page
// once early in a long-running chat and pay to resend its (up to 300KB)
// content on every single turn for the rest of that chat's life, forever —
// not just while the turn that attached it is still in the model's context.
// The bound is applied unconditionally, regardless of whether general
// history summarization (Service.historySummarizationEnabled) is on — that
// flag governs whether OLD TEXT gets compressed or resent verbatim, a much
// cheaper concern than resurfacing a whole HTML attachment on every future
// turn; this cap exists independently of it.
//
// priorMessages must be ordered oldest-first (see
// chat.Repository.ListMessagesByChat) — the same order doGenerate already
// receives them in.
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

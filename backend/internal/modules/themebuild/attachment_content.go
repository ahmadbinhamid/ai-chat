package themebuild

import (
	"context"

	"ai-chat/internal/modules/chat"
)

// GetAttachmentContent returns one attachment's raw bytes/media type/
// filename for the authenticated caller — the read path behind the
// byte-serving route the frontend needs now that GET /chat's transcript is
// metadata-only (see chat.MessageAttachment's own doc comment: Content is
// never serialized). Same ownership chain as RevertToMessage: tenant ->
// chat, then chat -> message — an attachment id that's real but belongs to
// a message outside this chat (or a chat outside this tenant) is
// indistinguishable from one that doesn't exist at all, same as every
// other 404-not-403 lookup in this codebase.
func (s *Service) GetAttachmentContent(ctx context.Context, tenantID uint64, chatID, messageID, attachmentID string) (chat.MessageAttachment, error) {
	if _, err := s.chats.GetChat(ctx, tenantID, chatID); err != nil {
		return chat.MessageAttachment{}, err
	}
	if _, err := s.chats.GetMessage(ctx, chatID, messageID); err != nil {
		return chat.MessageAttachment{}, err
	}

	attachments, err := s.chats.GetAttachmentsContent(ctx, messageID)
	if err != nil {
		return chat.MessageAttachment{}, err
	}
	for _, a := range attachments {
		if a.ID == attachmentID {
			return a, nil
		}
	}
	return chat.MessageAttachment{}, chat.ErrNotFound
}

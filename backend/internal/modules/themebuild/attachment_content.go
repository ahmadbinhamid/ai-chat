package themebuild

import (
	"context"

	"ai-chat/internal/modules/chat"
)

// GetAttachmentContent returns one attachment's raw bytes/media type/filename; an
// attachment belonging to another chat/tenant is treated as not-found, not forbidden.
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

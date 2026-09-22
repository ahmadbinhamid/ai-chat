package chat

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
)

// mysqlDuplicateEntry is MySQL error 1062 (ER_DUP_ENTRY) — a unique/primary
// key violation.
const mysqlDuplicateEntry = 1062

// Service holds chat/message business rules (ownership scoping, token totals) and
// delegates persistence to the repository.
type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

// GetOrCreateChat returns the tenant's one, ongoing chat of the given type,
// creating it on first use. chatType comes from the caller (themebuild
func (s *Service) GetOrCreateChat(ctx context.Context, tenantID uint64, chatType string) (Chat, error) {
	c, err := s.repo.GetChatByTenantAndType(ctx, tenantID, chatType)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Chat{}, err
	}

	c, err = s.createChat(ctx, tenantID, chatType)
	if err == nil {
		return c, nil
	}
	// Two concurrent first messages can race to insert; the loser re-reads rather than
	// surfacing the raw duplicate-key error.
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == mysqlDuplicateEntry {
		return s.repo.GetChatByTenantAndType(ctx, tenantID, chatType)
	}
	return Chat{}, err
}

func (s *Service) createChat(ctx context.Context, tenantID uint64, chatType string) (Chat, error) {
	now := time.Now().UTC()
	c := Chat{
		ID:        uuid.NewString(),
		TenantID:  tenantID,
		Type:      chatType,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.repo.CreateChat(ctx, c); err != nil {
		return Chat{}, err
	}
	return c, nil
}

// GetChatForTenant returns the tenant's one chat of the given type, or
// ErrNotFound if they haven't sent a first message yet — read-only, unlike
func (s *Service) GetChatForTenant(ctx context.Context, tenantID uint64, chatType string) (Chat, error) {
	return s.repo.GetChatByTenantAndType(ctx, tenantID, chatType)
}

// GetChat fetches a chat and checks it belongs to tenantID, returning ErrNotFound either
// way so a caller can't distinguish nonexistent from someone else's.
func (s *Service) GetChat(ctx context.Context, tenantID uint64, chatID string) (Chat, error) {
	c, err := s.repo.GetChatByID(ctx, chatID)
	if err != nil {
		return Chat{}, err
	}
	if c.TenantID != tenantID {
		return Chat{}, ErrNotFound
	}
	return c, nil
}

// ListMessages returns a chat's full turn history after verifying
// ownership. Prefer ListMessagesForVerifiedChat if the caller already
func (s *Service) ListMessages(ctx context.Context, tenantID uint64, chatID string) ([]Message, error) {
	if _, err := s.GetChat(ctx, tenantID, chatID); err != nil {
		return nil, err
	}
	return s.repo.ListMessagesByChat(ctx, chatID)
}

// ListMessagesForVerifiedChat skips ownership re-verification — only for a
// caller that already confirmed chatID belongs to this tenant this request.
func (s *Service) ListMessagesForVerifiedChat(ctx context.Context, chatID string) ([]Message, error) {
	return s.repo.ListMessagesByChat(ctx, chatID)
}

// GetMessage fetches a message and verifies it belongs to chatID (already tenant-verified
// by the caller) — used before applying a message's proposed changes.
func (s *Service) GetMessage(ctx context.Context, chatID, messageID string) (Message, error) {
	m, err := s.repo.GetMessageByID(ctx, messageID)
	if err != nil {
		return Message{}, err
	}
	if m.ChatID != chatID {
		return Message{}, ErrNotFound
	}
	return m, nil
}

// imageExtensions maps the media types the handler's oneof binding accepts
// to a filename extension — an unrecognized media type can't reach here.
var imageExtensions = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/gif":  "gif",
	"image/webp": "webp",
}

// filenameForImage synthesizes "image-N.ext" since MessageImage's wire
// format carries no client-supplied filename.
func filenameForImage(mediaType string, position int) string {
	ext := imageExtensions[mediaType]
	if ext == "" {
		ext = "bin"
	}
	return fmt.Sprintf("image-%d.%s", position+1, ext)
}

// buildAttachments decodes wire-format images/HTML into raw-bytes MessageAttachment rows,
// once, at the write boundary.
func buildAttachments(images []MessageImage, htmlAttachmentFilename, htmlAttachmentContent *string) ([]MessageAttachment, error) {
	var attachments []MessageAttachment
	now := time.Now().UTC()

	for i, img := range images {
		raw, err := base64.StdEncoding.DecodeString(img.Base64)
		if err != nil {
			return nil, fmt.Errorf("decode image %d: %w", i, err)
		}
		sum := sha256.Sum256(raw)
		attachments = append(attachments, MessageAttachment{
			ID:        uuid.NewString(),
			Kind:      AttachmentKindImage,
			Filename:  filenameForImage(img.MediaType, i),
			MediaType: img.MediaType,
			SizeBytes: int64(len(raw)),
			Checksum:  hex.EncodeToString(sum[:]),
			Position:  i,
			Content:   raw,
			CreatedAt: now,
		})
	}

	if htmlAttachmentFilename != nil && htmlAttachmentContent != nil {
		raw := []byte(*htmlAttachmentContent)
		sum := sha256.Sum256(raw)
		attachments = append(attachments, MessageAttachment{
			ID:        uuid.NewString(),
			Kind:      AttachmentKindHTML,
			Filename:  *htmlAttachmentFilename,
			MediaType: "text/html",
			SizeBytes: int64(len(raw)),
			Checksum:  hex.EncodeToString(sum[:]),
			Position:  0,
			Content:   raw,
			CreatedAt: now,
		})
	}

	return attachments, nil
}

// RecordUserMessage appends the merchant's prompt. Since a chat is shared by every user on
// the tenant, userName/userEmail attribute the turn to a person.
func (s *Service) RecordUserMessage(
	ctx context.Context, c Chat, userID *uint64, userName, userEmail, content string,
	images []MessageImage, htmlAttachmentFilename, htmlAttachmentContent *string,
) (Message, error) {
	now := time.Now().UTC()
	var namePtr *string
	if userName != "" {
		namePtr = &userName
	}
	var emailPtr *string
	if userEmail != "" {
		emailPtr = &userEmail
	}
	m := Message{
		ID:          uuid.NewString(),
		ChatID:      c.ID,
		TenantID:    c.TenantID,
		Role:        RoleUser,
		UserID:      userID,
		UserName:    namePtr,
		UserEmail:   emailPtr,
		Content:     content,
		Status:      MessageStatusCompleted,
		ApplyStatus: ApplyStatusNotApplicable,
		CreatedAt:   now,
	}
	attachments, err := buildAttachments(images, htmlAttachmentFilename, htmlAttachmentContent)
	if err != nil {
		return Message{}, fmt.Errorf("build attachments: %w", err)
	}
	for i := range attachments {
		attachments[i].MessageID = m.ID
		attachments[i].TenantID = m.TenantID
	}
	m.Attachments = attachments
	if err := s.repo.CreateMessageAndTouchUsage(ctx, m, attachments, 0, 0, now); err != nil {
		return Message{}, err
	}
	return m, nil
}

// GetAttachmentsContent returns messageID's attachments WITH their raw bytes.
func (s *Service) GetAttachmentsContent(ctx context.Context, messageID string) ([]MessageAttachment, error) {
	return s.repo.GetAttachmentsContent(ctx, messageID)
}

// AttachHTMLToMessage attaches an HTML reference to messageID after the fact, once a
// reference-URL fetch completes during generation. Idempotent: safe to retry after a crash.
func (s *Service) AttachHTMLToMessage(ctx context.Context, messageID string, tenantID uint64, filename, content string) error {
	raw := []byte(content)
	sum := sha256.Sum256(raw)
	return s.repo.UpsertHTMLAttachment(ctx, MessageAttachment{
		ID:        uuid.NewString(),
		MessageID: messageID,
		TenantID:  tenantID,
		Kind:      AttachmentKindHTML,
		Filename:  filename,
		MediaType: "text/html",
		SizeBytes: int64(len(raw)),
		Checksum:  hex.EncodeToString(sum[:]),
		Position:  0,
		Content:   raw,
		CreatedAt: time.Now().UTC(),
	})
}

// RecordManualEditMessage appends a bookkeeping turn for a file the merchant edited
// directly; uses RoleSystem since RoleAssistant would misattribute it to the model.
func (s *Service) RecordManualEditMessage(ctx context.Context, c Chat, filePath string) (Message, error) {
	now := time.Now().UTC()
	m := Message{
		ID:          uuid.NewString(),
		ChatID:      c.ID,
		TenantID:    c.TenantID,
		Role:        RoleSystem,
		Content:     "Edited " + filePath + " directly in the preview",
		Status:      MessageStatusCompleted,
		ApplyStatus: ApplyStatusPending,
		CreatedAt:   now,
	}
	if err := s.repo.CreateMessageAndTouchUsage(ctx, m, nil, 0, 0, now); err != nil {
		return Message{}, err
	}
	return m, nil
}

// RecordAssistantMessage appends the model's reply, rolling token usage into the chat's
// running totals. Also called for a failed generation (status MessageStatusFailed).
func (s *Service) RecordAssistantMessage(ctx context.Context, c Chat, content string, status MessageStatus, inputTokens, outputTokens int64, applyStatus ApplyStatus) (Message, error) {
	now := time.Now().UTC()
	m := Message{
		ID:           uuid.NewString(),
		ChatID:       c.ID,
		TenantID:     c.TenantID,
		Role:         RoleAssistant,
		Content:      content,
		Status:       status,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		ApplyStatus:  applyStatus,
		CreatedAt:    now,
	}
	if applyStatus == ApplyStatusApplied {
		m.AppliedAt = &now
	}
	if err := s.repo.CreateMessageAndTouchUsage(ctx, m, nil, inputTokens, outputTokens, now); err != nil {
		return Message{}, err
	}
	return m, nil
}

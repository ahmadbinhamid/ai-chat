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

// Service holds the chat/message business rules — ownership scoping and
// keeping a chat's running token totals in sync — and delegates persistence
// to the repository.
type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

// GetOrCreateChat returns the tenant's one, ongoing chat of the given type —
// creating it on first use. chatType is supplied by the caller (themebuild
// passes "builder") rather than owned by this package, which is what keeps
// this package usable for an unrelated chat use case on the same tenant later.
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
	// Two concurrent first messages for the same tenant (two tabs, a
	// client retry racing the original) can both miss the lookup above and
	// race to insert — uniq_chats_tenant_type means exactly one wins. The
	// loser isn't a real failure, it's just "the chat already exists", so
	// re-read it rather than surfacing the raw duplicate-key error.
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
// ErrNotFound if they haven't sent a first message yet — a normal, expected
// state for a caller to handle (e.g. GET /chat showing the empty/greeting
// state), not necessarily an error to surface as one. Read-only: unlike
// GetOrCreateChat, this never creates a row.
func (s *Service) GetChatForTenant(ctx context.Context, tenantID uint64, chatType string) (Chat, error) {
	return s.repo.GetChatByTenantAndType(ctx, tenantID, chatType)
}

// GetChat fetches a chat and checks it belongs to tenantID, returning
// ErrNotFound (not a distinct "forbidden") either way a caller can't tell a
// chat that doesn't exist from one that belongs to someone else — the same
// 404-not-403 ownership pattern used throughout this codebase.
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

// ListMessages returns a chat's full turn history, after verifying
// ownership. Costs an extra GetChatByID query beyond the list itself — if
// the caller already holds a chat it fetched (and tenant-verified) moments
// ago in the same request, prefer ListMessagesForVerifiedChat instead of
// paying for that re-check twice.
func (s *Service) ListMessages(ctx context.Context, tenantID uint64, chatID string) ([]Message, error) {
	if _, err := s.GetChat(ctx, tenantID, chatID); err != nil {
		return nil, err
	}
	return s.repo.ListMessagesByChat(ctx, chatID)
}

// ListMessagesForVerifiedChat returns a chat's full turn history without
// re-verifying ownership — only for a caller that has already confirmed
// chatID belongs to the requesting tenant in this same request (e.g.
// ChatHandler.Get, right after its own GetChatForTenant call).
func (s *Service) ListMessagesForVerifiedChat(ctx context.Context, chatID string) ([]Message, error) {
	return s.repo.ListMessagesByChat(ctx, chatID)
}

// GetMessage fetches a message and verifies it belongs to the given chat
// (which the caller has already verified belongs to tenantID) — used before
// applying a message's proposed changes.
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

// imageExtensions maps the exact media types sendMessageRequest.Images
// accepts (see the handler's oneof binding) to a filename extension — used
// only by filenameForImage. Deliberately the same set the handler validates
// against: an unrecognized media type can't reach here.
var imageExtensions = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/gif":  "gif",
	"image/webp": "webp",
}

// filenameForImage derives a stable, deterministic filename for an attached
// image — the wire format (MessageImage) carries only base64 + media_type,
// no client-supplied name (neither a file picker nor a clipboard paste
// gives one), so one must be synthesized. "image-N.ext" where N is the
// image's 1-based position in this message and ext comes from media_type —
// the same rule the 20260909000002 migration's backfill uses for existing
// rows (ord, JSON_TABLE's own 1-based index, directly as N), so a
// backfilled row and one attached going forward name themselves
// identically.
func filenameForImage(mediaType string, position int) string {
	ext := imageExtensions[mediaType]
	if ext == "" {
		ext = "bin"
	}
	return fmt.Sprintf("image-%d.%s", position+1, ext)
}

// buildAttachments decodes the wire-format images/HTML attachment (base64
// and plain text respectively — see MessageImage's own doc comment) into
// the raw-bytes MessageAttachment rows RecordUserMessage persists. Base64
// decoding happens here, once, at the write boundary — every downstream
// consumer (including themebuild.Service.doGenerate's re-resolution) works
// with already-decoded bytes and re-encodes only transiently, right before
// an API call.
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

// RecordUserMessage appends the merchant's prompt to the thread and folds
// it into the chat's recency ordering (no tokens are billed for a user
// turn, so the running totals are untouched). Since a chat is now shared by
// every user on the tenant (see GetOrCreateChat), userName/userEmail are
// what let the transcript attribute this turn to a person instead of a
// generic "You". images/htmlAttachment* are the wire-format attachment(s),
// if any — decoded and persisted as chat_message_attachments rows (see
// buildAttachments), the only representation now (chat_messages no longer
// carries any attachment columns of its own).
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

// GetAttachmentsContent returns messageID's attachments WITH their raw
// bytes — see Repository.GetAttachmentsContent's own doc comment. The only
// caller is themebuild.Service.doGenerate, and only when it already knows
// (from a metadata-only Message.Attachments it just loaded) that this
// message actually has attachments to fetch.
func (s *Service) GetAttachmentsContent(ctx context.Context, messageID string) ([]MessageAttachment, error) {
	return s.repo.GetAttachmentsContent(ctx, messageID)
}

// RecordManualEditMessage appends a bookkeeping turn for a file the merchant
// edited directly in the preview (see themebuild.Service.SaveManualEdit) —
// every chat_generated_files row needs a message_id to hang off (foreign
// key), and this didn't come from the model, so RoleAssistant would
// misattribute it as something Claude said. Uses RoleSystem (defined
// alongside RoleUser/RoleAssistant, previously unused) rather than adding a
// new role — this is exactly the "not a conversation turn" case it exists
// for. ApplyStatusPending because, like a generation turn, its file exists
// only in the draft overlay until Apply.
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

// RecordAssistantMessage appends the model's reply, rolling its token usage
// into the chat's running totals. applyStatus should be ApplyStatusPending
// when the turn's proposed changes were staged into the draft overlay (the
// normal case now that generation defers writing to the real theme — see
// themebuild's package doc comment), ApplyStatusNotApplicable when it
// proposed none; ApplyStatusApplied is stamped later, in bulk, by
// Service.ApplyDraft's own UPDATE, not through this function. Also called
// for a failed generation (status MessageStatusFailed) — see
// chat.MessageStatusFailed's doc comment — not just a turn that completed.
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

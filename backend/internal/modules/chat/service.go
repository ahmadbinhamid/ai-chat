package chat

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

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
	if errors.Is(err, ErrNotFound) {
		return s.createChat(ctx, tenantID, chatType)
	}
	return c, err
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
// ownership.
func (s *Service) ListMessages(ctx context.Context, tenantID uint64, chatID string) ([]Message, error) {
	if _, err := s.GetChat(ctx, tenantID, chatID); err != nil {
		return nil, err
	}
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

// RecordUserMessage appends the merchant's prompt to the thread and folds
// it into the chat's recency ordering (no tokens are billed for a user
// turn, so the running totals are untouched). Since a chat is now shared by
// every user on the tenant (see GetOrCreateChat), userName is what lets the
// transcript attribute this turn to a person instead of a generic "You".
func (s *Service) RecordUserMessage(ctx context.Context, c Chat, userID *uint64, userName, content string) (Message, error) {
	now := time.Now().UTC()
	var namePtr *string
	if userName != "" {
		namePtr = &userName
	}
	m := Message{
		ID:          uuid.NewString(),
		ChatID:      c.ID,
		TenantID:    c.TenantID,
		Role:        RoleUser,
		UserID:      userID,
		UserName:    namePtr,
		Content:     content,
		Status:      MessageStatusCompleted,
		ApplyStatus: ApplyStatusNotApplicable,
		CreatedAt:   now,
	}
	if err := s.repo.CreateMessage(ctx, m); err != nil {
		return Message{}, err
	}
	if err := s.repo.TouchChatUsage(ctx, c.ID, 0, 0, now); err != nil {
		return Message{}, err
	}
	return m, nil
}

// RecordAssistantMessage appends the model's reply, rolling its token usage
// into the chat's running totals. applyStatus should be ApplyStatusApplied
// when the turn's proposed changes were already written to the real theme,
// ApplyStatusNotApplicable otherwise.
func (s *Service) RecordAssistantMessage(ctx context.Context, c Chat, content string, status MessageStatus, errMsg *string, inputTokens, outputTokens int64, applyStatus ApplyStatus) (Message, error) {
	now := time.Now().UTC()
	m := Message{
		ID:           uuid.NewString(),
		ChatID:       c.ID,
		TenantID:     c.TenantID,
		Role:         RoleAssistant,
		Content:      content,
		Status:       status,
		ErrorMessage: errMsg,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		ApplyStatus:  applyStatus,
		CreatedAt:    now,
	}
	if err := s.repo.CreateMessage(ctx, m); err != nil {
		return Message{}, err
	}
	if err := s.repo.TouchChatUsage(ctx, c.ID, inputTokens, outputTokens, now); err != nil {
		return Message{}, err
	}
	return m, nil
}

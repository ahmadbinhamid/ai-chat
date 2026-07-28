package chat

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrNotFound is returned when a chat or message row doesn't exist — or,
// from the service layer, when it exists but belongs to a different tenant
// (see Service.GetChat): the caller can't tell the two apart, which is the
// point.
var ErrNotFound = errors.New("chat not found")

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) CreateChat(ctx context.Context, c Chat) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO chats (id, tenant_id, type, total_input_tokens, total_output_tokens, last_message_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, c.ID, c.TenantID, c.Type, c.TotalInputTokens, c.TotalOutputTokens, c.LastMessageAt, c.CreatedAt, c.UpdatedAt)
	return err
}

func (r *Repository) GetChatByID(ctx context.Context, id string) (Chat, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, type, total_input_tokens, total_output_tokens, last_message_at, created_at, updated_at
		FROM chats WHERE id = ?
	`, id)
	return scanChat(row)
}

// GetChatByTenantAndType returns the tenant's one chat of the given type
// (see uniq_chats_tenant_type — there's never more than one row to
// disambiguate), or ErrNotFound if the tenant hasn't sent a first message of
// that type yet.
func (r *Repository) GetChatByTenantAndType(ctx context.Context, tenantID uint64, chatType string) (Chat, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, type, total_input_tokens, total_output_tokens, last_message_at, created_at, updated_at
		FROM chats WHERE tenant_id = ? AND type = ?
	`, tenantID, chatType)
	return scanChat(row)
}

// TouchChatUsage adds to the chat's running token totals and refreshes
// last_message_at/updated_at — called once per turn (user or assistant),
// so the sidebar can show recency and total spend without summing
// chat_messages.
func (r *Repository) TouchChatUsage(ctx context.Context, chatID string, inputTokens, outputTokens int64, at time.Time) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE chats
		SET total_input_tokens = total_input_tokens + ?,
		    total_output_tokens = total_output_tokens + ?,
		    last_message_at = ?,
		    updated_at = ?
		WHERE id = ?
	`, inputTokens, outputTokens, at, at, chatID)
	if err != nil {
		return err
	}
	return checkAffected(res)
}

func (r *Repository) CreateMessage(ctx context.Context, m Message) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO chat_messages (id, chat_id, tenant_id, role, user_id, user_name, content, status, error_message, input_tokens, output_tokens, apply_status, applied_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, m.ID, m.ChatID, m.TenantID, m.Role, m.UserID, m.UserName, m.Content, m.Status, m.ErrorMessage, m.InputTokens, m.OutputTokens, m.ApplyStatus, m.AppliedAt, m.CreatedAt)
	return err
}

func (r *Repository) ListMessagesByChat(ctx context.Context, chatID string) ([]Message, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, chat_id, tenant_id, role, user_id, user_name, content, status, error_message, input_tokens, output_tokens, apply_status, applied_at, created_at
		FROM chat_messages WHERE chat_id = ? ORDER BY created_at ASC
	`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

func (r *Repository) GetMessageByID(ctx context.Context, id string) (Message, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, chat_id, tenant_id, role, user_id, user_name, content, status, error_message, input_tokens, output_tokens, apply_status, applied_at, created_at
		FROM chat_messages WHERE id = ?
	`, id)
	return scanMessage(row)
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanChat(s scanner) (Chat, error) {
	var c Chat
	err := s.Scan(&c.ID, &c.TenantID, &c.Type,
		&c.TotalInputTokens, &c.TotalOutputTokens, &c.LastMessageAt, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Chat{}, ErrNotFound
	}
	return c, err
}

func scanMessage(s scanner) (Message, error) {
	var m Message
	err := s.Scan(&m.ID, &m.ChatID, &m.TenantID, &m.Role, &m.UserID, &m.UserName, &m.Content, &m.Status,
		&m.ErrorMessage, &m.InputTokens, &m.OutputTokens, &m.ApplyStatus, &m.AppliedAt, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	return m, err
}

func checkAffected(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

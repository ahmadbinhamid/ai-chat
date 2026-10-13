package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// ErrNotFound means a row doesn't exist, or (from the service layer) belongs to a
// different tenant — the caller can't tell the two apart, on purpose.
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

// GetChatByTenantAndType returns the tenant's one chat of the given type, or ErrNotFound
// if the tenant hasn't sent a first message of that type yet.
func (r *Repository) GetChatByTenantAndType(ctx context.Context, tenantID uint64, chatType string) (Chat, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, type, total_input_tokens, total_output_tokens, last_message_at, created_at, updated_at
		FROM chats WHERE tenant_id = ? AND type = ?
	`, tenantID, chatType)
	return scanChat(row)
}

// execer is satisfied by both *sql.DB and *sql.Tx, letting these helpers run inside one transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func touchChatUsage(ctx context.Context, e execer, chatID string, inputTokens, outputTokens int64, at time.Time) error {
	res, err := e.ExecContext(ctx, `
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

func createMessage(ctx context.Context, e execer, m Message) error {
	_, err := e.ExecContext(ctx, `
		INSERT INTO chat_messages (id, chat_id, tenant_id, role, user_id, user_name, user_email, content, status, input_tokens, output_tokens, model_id, effort, cost_usd, apply_status, applied_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, m.ID, m.ChatID, m.TenantID, m.Role, m.UserID, m.UserName, m.UserEmail, m.Content, m.Status, m.InputTokens, m.OutputTokens, m.ModelID, m.Effort, m.CostUSD, m.ApplyStatus, m.AppliedAt, m.CreatedAt, m.CreatedAt)
	return err
}

// createAttachments inserts one row per attachment; the count is bounded (enforced by
// Service.Generate), so a per-row loop is simpler than a multi-row INSERT for no real benefit.
func createAttachments(ctx context.Context, e execer, attachments []MessageAttachment) error {
	for _, a := range attachments {
		_, err := e.ExecContext(ctx, `
			INSERT INTO chat_message_attachments (id, message_id, tenant_id, kind, filename, media_type, size_bytes, checksum, position, content, storage_key, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, a.ID, a.MessageID, a.TenantID, a.Kind, a.Filename, a.MediaType, a.SizeBytes, a.Checksum, a.Position, a.Content, a.StorageKey, a.CreatedAt, a.CreatedAt)
		if err != nil {
			return err
		}
	}
	return nil
}

// AddStockImageAttachment stores a downloaded stock photo on messageID at the next free stock_image position.
func (r *Repository) AddStockImageAttachment(ctx context.Context, a MessageAttachment) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO chat_message_attachments (id, message_id, tenant_id, kind, filename, media_type, size_bytes, checksum, position, content, created_at, updated_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, COALESCE(MAX(position) + 1, 0), ?, ?, ?
		FROM chat_message_attachments
		WHERE message_id = ? AND kind = ?
	`, a.ID, a.MessageID, a.TenantID, string(AttachmentKindStockImage), a.Filename, a.MediaType, a.SizeBytes, a.Checksum,
		a.Content, a.CreatedAt, a.CreatedAt, a.MessageID, string(AttachmentKindStockImage))
	return err
}

// UpsertHTMLAttachment replaces any existing row at the same (message_id, kind, position).
func (r *Repository) UpsertHTMLAttachment(ctx context.Context, a MessageAttachment) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO chat_message_attachments (id, message_id, tenant_id, kind, filename, media_type, size_bytes, checksum, position, content, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			filename = VALUES(filename), media_type = VALUES(media_type), size_bytes = VALUES(size_bytes),
			checksum = VALUES(checksum), content = VALUES(content), updated_at = VALUES(updated_at)
	`, a.ID, a.MessageID, a.TenantID, a.Kind, a.Filename, a.MediaType, a.SizeBytes, a.Checksum, a.Position, a.Content, a.CreatedAt, a.CreatedAt)
	return err
}

// CreateMessageAndTouchUsage does all three writes in one transaction — a message existing
// without its chat's token totals reflecting it would be a subtly-inconsistent state.
func (r *Repository) CreateMessageAndTouchUsage(ctx context.Context, m Message, attachments []MessageAttachment, inputTokens, outputTokens int64, at time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once Commit succeeds below

	if err := createMessage(ctx, tx, m); err != nil {
		return err
	}
	if err := createAttachments(ctx, tx, attachments); err != nil {
		return err
	}
	if err := touchChatUsage(ctx, tx, m.ChatID, inputTokens, outputTokens, at); err != nil {
		return err
	}
	return tx.Commit()
}

// CreateMessageAndTouchUsageTx is CreateMessageAndTouchUsage inside the caller's transaction, for a turn whose
// reply must commit together with other modules' writes.
func (r *Repository) CreateMessageAndTouchUsageTx(ctx context.Context, tx *sql.Tx, m Message, inputTokens, outputTokens int64, at time.Time) error {
	if err := createMessage(ctx, tx, m); err != nil {
		return err
	}
	return touchChatUsage(ctx, tx, m.ChatID, inputTokens, outputTokens, at)
}

// ListMessagesByChat returns full turn history with attachment METADATA only — this backs
func (r *Repository) ListMessagesByChat(ctx context.Context, chatID string) ([]Message, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, chat_id, tenant_id, role, user_id, user_name, user_email, content, status, input_tokens, output_tokens, model_id, effort, apply_status, applied_at, created_at
		FROM chat_messages WHERE chat_id = ? ORDER BY created_at ASC
	`, chatID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var messages []Message
	var ids []string
	for rows.Next() {
		m, err := scanMessageBasic(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, m)
		ids = append(ids, m.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return messages, nil
	}

	attachmentsByMessage, err := r.listAttachmentMetadata(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range messages {
		messages[i].Attachments = attachmentsByMessage[messages[i].ID]
	}
	return messages, nil
}

// listAttachmentMetadata batches attachment metadata for every message id in one query,
// never one per message. Returns nil immediately, no query, when messageIDs is empty.
func (r *Repository) listAttachmentMetadata(ctx context.Context, messageIDs []string) (map[string][]MessageAttachment, error) {
	if len(messageIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(messageIDs))
	args := make([]any, len(messageIDs))
	for i, id := range messageIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	// kind before position: position is scoped per kind, so an image and the HTML attachment
	// can share position 0 — ordering by kind first keeps that tie deterministic.
	query := fmt.Sprintf(`
		SELECT id, message_id, tenant_id, kind, filename, media_type, size_bytes, checksum, position, storage_key, created_at
		FROM chat_message_attachments
		WHERE message_id IN (%s) AND kind <> ?
		ORDER BY message_id, kind, position
	`, strings.Join(placeholders, ","))
	args = append(args, string(AttachmentKindStockImage))
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	byMessage := make(map[string][]MessageAttachment)
	for rows.Next() {
		var a MessageAttachment
		var kind string
		if err := rows.Scan(&a.ID, &a.MessageID, &a.TenantID, &kind, &a.Filename, &a.MediaType,
			&a.SizeBytes, &a.Checksum, &a.Position, &a.StorageKey, &a.CreatedAt); err != nil {
			return nil, err
		}
		k := AttachmentKind(kind)
		if !k.known() {
			// An unknown kind must not crash a transcript read — skip it, but log it.
			slog.Warn("chat_message_attachments: skipping row with unknown kind", "kind", kind, "attachment_id", a.ID)
			continue
		}
		a.Kind = k
		byMessage[a.MessageID] = append(byMessage[a.MessageID], a)
	}
	return byMessage, rows.Err()
}

// GetAttachmentsContent returns messageID's attachments WITH raw decoded bytes in Content —
// the one read path here that pulls bytes out of MySQL. Only used by doGenerate, never for a transcript read.
func (r *Repository) GetAttachmentsContent(ctx context.Context, messageID string) ([]MessageAttachment, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, message_id, tenant_id, kind, filename, media_type, size_bytes, checksum, position, content, storage_key, created_at
		FROM chat_message_attachments
		WHERE message_id = ? AND kind <> ?
		ORDER BY kind, position
	`, messageID, string(AttachmentKindStockImage))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var attachments []MessageAttachment
	for rows.Next() {
		var a MessageAttachment
		var kind string
		if err := rows.Scan(&a.ID, &a.MessageID, &a.TenantID, &kind, &a.Filename, &a.MediaType,
			&a.SizeBytes, &a.Checksum, &a.Position, &a.Content, &a.StorageKey, &a.CreatedAt); err != nil {
			return nil, err
		}
		k := AttachmentKind(kind)
		if !k.known() {
			slog.Warn("chat_message_attachments: skipping row with unknown kind", "kind", kind, "attachment_id", a.ID)
			continue
		}
		if a.Content == nil && a.StorageKey == nil {
			// Shouldn't be possible — treat as corrupt data, not a crash.
			slog.Warn("chat_message_attachments: skipping row with neither content nor storage_key", "attachment_id", a.ID)
			continue
		}
		a.Kind = k
		attachments = append(attachments, a)
	}
	return attachments, rows.Err()
}

// GetChatImageAttachment returns one image attachment WITH its bytes, only if it belongs to a message in chatID.
func (r *Repository) GetChatImageAttachment(ctx context.Context, chatID, attachmentID string) (MessageAttachment, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT a.id, a.message_id, a.tenant_id, a.filename, a.media_type, a.size_bytes, a.checksum, a.position, a.content, a.storage_key, a.created_at
		FROM chat_message_attachments a
		JOIN chat_messages m ON m.id = a.message_id
		WHERE a.id = ? AND m.chat_id = ? AND a.kind IN (?, ?)
	`, attachmentID, chatID, string(AttachmentKindImage), string(AttachmentKindStockImage))
	var a MessageAttachment
	err := row.Scan(&a.ID, &a.MessageID, &a.TenantID, &a.Filename, &a.MediaType, &a.SizeBytes, &a.Checksum,
		&a.Position, &a.Content, &a.StorageKey, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return MessageAttachment{}, ErrNotFound
	}
	if err != nil {
		return MessageAttachment{}, err
	}
	if a.Content == nil {
		return MessageAttachment{}, fmt.Errorf("attachment %s has no stored content", attachmentID)
	}
	a.Kind = AttachmentKindImage
	return a, nil
}

// ListChatImageHeads returns the first n bytes of every image attachment in chatID, keyed by attachment ID — enough to
// read dimensions without pulling whole images out of MySQL.
func (r *Repository) ListChatImageHeads(ctx context.Context, chatID string, n int) (map[string][]byte, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT a.id, SUBSTRING(a.content, 1, ?)
		FROM chat_message_attachments a
		JOIN chat_messages m ON m.id = a.message_id
		WHERE m.chat_id = ? AND a.kind = ? AND a.content IS NOT NULL
	`, n, chatID, string(AttachmentKindImage))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	heads := make(map[string][]byte)
	for rows.Next() {
		var id string
		var head []byte
		if err := rows.Scan(&id, &head); err != nil {
			return nil, err
		}
		heads[id] = head
	}
	return heads, rows.Err()
}

// GetMessageByID deliberately does NOT select/join attachment data; its only caller needs
// just ApplyStatus/CreatedAt. Add a listAttachmentMetadata call if a future caller needs attachments.
func (r *Repository) GetMessageByID(ctx context.Context, id string) (Message, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, chat_id, tenant_id, role, user_id, user_name, user_email, content, status, input_tokens, output_tokens, model_id, effort, apply_status, applied_at, created_at
		FROM chat_messages WHERE id = ?
	`, id)
	return scanMessageBasic(row)
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

// scanMessageBasic scans a chat_messages row's own columns only; attachments live in a
// separate table, loaded separately by a caller that explicitly asks.
func scanMessageBasic(s scanner) (Message, error) {
	var m Message
	err := s.Scan(&m.ID, &m.ChatID, &m.TenantID, &m.Role, &m.UserID, &m.UserName, &m.UserEmail, &m.Content, &m.Status,
		&m.InputTokens, &m.OutputTokens, &m.ModelID, &m.Effort, &m.ApplyStatus, &m.AppliedAt, &m.CreatedAt)
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

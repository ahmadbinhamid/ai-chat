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

// execer is satisfied by both *sql.DB and *sql.Tx — lets touchChatUsage/
// createMessage/createAttachments run together inside one transaction (see
// CreateMessageAndTouchUsage, the only caller of any of them).
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
		INSERT INTO chat_messages (id, chat_id, tenant_id, role, user_id, user_name, user_email, content, status, input_tokens, output_tokens, apply_status, applied_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, m.ID, m.ChatID, m.TenantID, m.Role, m.UserID, m.UserName, m.UserEmail, m.Content, m.Status, m.InputTokens, m.OutputTokens, m.ApplyStatus, m.AppliedAt, m.CreatedAt, m.CreatedAt)
	return err
}

// createAttachments inserts one row per attachment — bounded (at most
// maxImagesPerMessage images + one HTML file per message, enforced by
// themebuild.Service.Generate before this is ever reached), so a per-row
// loop over one transaction is simpler than building a multi-row INSERT for
// no real benefit at this size. updated_at is set to a.CreatedAt at insert
// (this table is still append-only — see the 20260909000005 migration's own
// doc comment for why it has the column anyway).
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

// UpsertHTMLAttachment inserts messageID's HTML attachment row, replacing
// any existing one at the same (message_id, kind, position) via ON
// DUPLICATE KEY UPDATE against uq_cma_message_kind_position — see
// Service.AttachHTMLToMessage's own doc comment for why this must be an
// upsert, not a plain insert: a generation restarted (by the reaper, after
// a crash mid-fetch) must not fail this call with a duplicate-key error the
// second time around, it should just replace the row with whatever content
// this attempt fetched.
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

// CreateMessageAndTouchUsage does all three writes in one transaction: a
// message row (and its attachments, if any) existing without its chat's
// running token totals reflecting it (or vice versa, if these ran as
// separate statements and a later one failed) would be a subtly-inconsistent
// state, not just a dropped side effect.
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

// ListMessagesByChat returns a chat's full turn history with attachment
// METADATA only (see MessageAttachment's own doc comment) — this is what
// backs GET /chat, which runs on every page load, so it must never carry a
// turn's attached bytes. Attachment metadata is loaded in one extra query
// (an IN-list over every message id in this chat), not a JOIN against
// chat_messages: a LEFT JOIN would fan out each message row once per
// attachment (and once with NULLs for the common zero-attachment case),
// pushing dedup logic onto the scan side; a second, narrow query keeps both
// queries simple to reason about independently, and is skipped entirely
// when the chat has no messages at all (see listAttachmentMetadata).
func (r *Repository) ListMessagesByChat(ctx context.Context, chatID string) ([]Message, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, chat_id, tenant_id, role, user_id, user_name, user_email, content, status, input_tokens, output_tokens, apply_status, applied_at, created_at
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

// listAttachmentMetadata batches attachment metadata (no content) for every
// message id given, in one query — never one query per message. Returns nil
// immediately, with no query at all, when messageIDs is empty (an empty
// chat costs nothing beyond the messages query that found it empty).
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
	// kind before position: position is scoped per kind, not per message
	// (images are 0..maxImagesPerMessage-1, the one allowed HTML file is
	// always 0 — see MessageAttachment's own doc comment), so an image and
	// the HTML attachment on the same message can share position 0. ORDER
	// BY message_id, position alone isn't a total order for that tie —
	// which kind sorts first would be unspecified/row-storage-dependent.
	// kind makes it deterministic; the frontend renders images and the
	// HTML chip as separate lists anyway; nothing depends on an
	// interleaved cross-kind order.
	query := fmt.Sprintf(`
		SELECT id, message_id, tenant_id, kind, filename, media_type, size_bytes, checksum, position, storage_key, created_at
		FROM chat_message_attachments
		WHERE message_id IN (%s)
		ORDER BY message_id, kind, position
	`, strings.Join(placeholders, ","))
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
		if k != AttachmentKindImage && k != AttachmentKindHTML {
			// A kind this build doesn't know (a future version, or bad
			// data) must not crash a transcript read — skip it, but make
			// it visible.
			slog.Warn("chat_message_attachments: skipping row with unknown kind", "kind", kind, "attachment_id", a.ID)
			continue
		}
		a.Kind = k
		byMessage[a.MessageID] = append(byMessage[a.MessageID], a)
	}
	return byMessage, rows.Err()
}

// GetAttachmentsContent returns messageID's attachments WITH their raw
// (decoded) bytes in Content — the one read path in this package that pulls
// bytes out of MySQL. Its only caller is themebuild.Service.doGenerate,
// immediately before it needs to actually send this turn's attachment(s) to
// the model — never used for a transcript read (see ListMessagesByChat).
func (r *Repository) GetAttachmentsContent(ctx context.Context, messageID string) ([]MessageAttachment, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, message_id, tenant_id, kind, filename, media_type, size_bytes, checksum, position, content, storage_key, created_at
		FROM chat_message_attachments
		WHERE message_id = ?
		ORDER BY kind, position
	`, messageID)
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
		if k != AttachmentKindImage && k != AttachmentKindHTML {
			slog.Warn("chat_message_attachments: skipping row with unknown kind", "kind", kind, "attachment_id", a.ID)
			continue
		}
		if a.Content == nil && a.StorageKey == nil {
			// Shouldn't be possible (every write path sets exactly one) —
			// treat as corrupt data, not a crash.
			slog.Warn("chat_message_attachments: skipping row with neither content nor storage_key", "attachment_id", a.ID)
			continue
		}
		a.Kind = k
		attachments = append(attachments, a)
	}
	return attachments, rows.Err()
}

// GetMessageByID deliberately does NOT select/join attachment data — its
// only caller (themebuild.Service.RevertToMessage, via chat.Service's own
// GetMessage) only ever reads a message's ApplyStatus/CreatedAt to decide
// how to revert, never its attachments. If a future caller of
// GetMessageByID DOES need attachment metadata, add a call to
// listAttachmentMetadata for the one resulting id rather than joining it
// into this query unconditionally.
func (r *Repository) GetMessageByID(ctx context.Context, id string) (Message, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, chat_id, tenant_id, role, user_id, user_name, user_email, content, status, input_tokens, output_tokens, apply_status, applied_at, created_at
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

// scanMessageBasic scans a chat_messages row's own columns — never
// attachments, which live in a separate table and are loaded (metadata
// only, or with content) by a caller that explicitly asks — see
// listAttachmentMetadata/GetAttachmentsContent. Shared by both
// ListMessagesByChat and GetMessageByID: neither needs anything beyond
// this row's own columns to do its job.
func scanMessageBasic(s scanner) (Message, error) {
	var m Message
	err := s.Scan(&m.ID, &m.ChatID, &m.TenantID, &m.Role, &m.UserID, &m.UserName, &m.UserEmail, &m.Content, &m.Status,
		&m.InputTokens, &m.OutputTokens, &m.ApplyStatus, &m.AppliedAt, &m.CreatedAt)
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

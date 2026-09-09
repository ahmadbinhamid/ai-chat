package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20260909000004_drop_html_attachment_columns",
		Up:   Up_20260909000004,
		Down: Down_20260909000004,
	})
}

// images, html_attachment_filename and html_attachment_content (added by
// the 20260908000001 and 20260909000001 migrations) are all dead:
// chat_message_attachments (the 20260909000002 migration) replaced every
// one of them, and by this point nothing in Go writes, selects, scans, or
// serializes any of the three — confirmed by tracing createMessage's
// INSERT list, both scan functions (scanMessageBasic/
// scanMessageWithAttachments), and every JSON tag on chat.Message before
// writing this migration. images briefly carried a dual-write compatibility
// shim (see chat.Message's own history) so tenant-dashboard could keep
// reading inlined base64 from GET /chat while it migrated to the
// attachments[] byte route; that shim is now removed on the Go side too —
// this feature has never shipped past one local branch, so there was no
// deployed frontend to protect from the cutover, and carrying the shim
// forward would only be a thing to remember to remove later. Every image
// attachment now lives exclusively in chat_message_attachments.
//
// themebuild.GenerateInput still has Images/HTMLAttachmentFilename/
// HTMLAttachmentContent fields — those are the unrelated request-path
// carriers (HTTP handler -> Generate, which validates and persists to
// chat_message_attachments), not columns on this table, and aren't
// affected by this migration.
//
// This migration originally (see its name) dropped only the two HTML
// columns; it now drops images too rather than shipping a second
// drop-images migration later, since the same "add-then-drop is a net
// no-op, keep both for honest history" reasoning from the HTML columns
// applies identically to images and the two ADD migrations
// (20260908000001, 20260909000001) are already committed/pushed and are
// not being touched. The file keeps its original name/timestamp
// (20260909000004) rather than being renamed to something like
// "drop_legacy_attachment_columns": on the database this was developed
// against, this migration's name is already recorded as applied (from
// when it only dropped the two HTML columns) — renaming it would register
// a new, distinct name the migrator has never seen, which would then try
// to run this (now three-column) Up from scratch and fail immediately on
// the two ALTER TABLE ... DROP COLUMN clauses for columns already gone.
// Safe for a genuinely fresh database either way; not safe for one that's
// already run this migration under its current name.
//
// chk_chat_messages_user_role (see the 20260727000002 migration) only
// constrains role/user_id — unaffected by dropping these three columns.
//
// Down re-adds all three with their original types, in their original
// relative order (images AFTER created_at, html_attachment_filename AFTER
// images, html_attachment_content AFTER html_attachment_filename — see the
// 20260908000001/20260909000001 migrations), so a rollback restores the
// SCHEMA. It cannot restore the DATA: none of the three columns' content is
// retained anywhere Down could read it back from — a rollback after this
// has run gets three empty columns, not the original values. Unlike an
// earlier draft of this migration's own comment claimed, this is a genuine,
// one-way data loss for any row that actually had content in these columns
// — the 20260909000002 migration's backfill (which used to copy that
// content into chat_message_attachments first) has been removed, since no
// database this feature has ever run against (including production, never
// deployed) had real data in these columns to preserve. If that's no
// longer true when this actually runs somewhere, stop and write a backfill
// migration first rather than trusting this comment.
func Up_20260909000004(db *sql.DB) error {
	_, err := db.Exec(`
		ALTER TABLE chat_messages
		DROP COLUMN images,
		DROP COLUMN html_attachment_filename,
		DROP COLUMN html_attachment_content
	`)
	return err
}

func Down_20260909000004(db *sql.DB) error {
	_, err := db.Exec(`
		ALTER TABLE chat_messages
		ADD COLUMN images LONGTEXT NULL AFTER created_at,
		ADD COLUMN html_attachment_filename VARCHAR(255) NULL AFTER images,
		ADD COLUMN html_attachment_content LONGTEXT NULL AFTER html_attachment_filename
	`)
	return err
}

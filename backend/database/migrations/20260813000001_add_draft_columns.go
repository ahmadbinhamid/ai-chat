package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20260813000001_add_draft_columns",
		Up:   Up_20260813000001,
		Down: Down_20260813000001,
	})
}

// This migration is what makes deferred writes (see themebuild's package
// doc comment: generation now stages a draft instead of writing
// immediately) safe against two specific data-loss bugs that only exist
// once a write is deferred:
//
// kind. commitWritePlan writes plan.layoutStart/plan.layoutEnd (the
// layout-start.liquid/layout-end.liquid splices a turn's
// LayoutLinksToAdd/LayoutScriptsToAdd produce) but persistFileRecords has
// always deliberately skipped auditing them — correct while writes were
// immediate, since there was nothing to lose: the splice already landed on
// the real theme. The moment the write is deferred, an unaudited layout
// splice is a bug: a CSS file the model adds would never get its <link>
// registered at apply time, because nothing durable remembers there was
// ever a splice to apply. kind tags every audited row 'proposed' (the
// model's own files) or 'layout' (a splice) so Service.ApplyDraft can tell
// them apart and merchant-facing file lists (GET /chat's pending_changes,
// GeneratedFilesCard) can filter 'layout' rows out — a splice isn't
// something a merchant asked for or should see as "a file", but it must
// still exist in the audit trail or it's lost.
//
// page_meta. PageMeta (pages.json title/slug/SEO fields) is derived from
// the model's PageRegistryEntry inside buildWritePlan and consumed
// immediately by commitWritePlan's WriteFile call — while writes were
// immediate, that's the only place it ever needed to exist. Deferred, it's
// gone by the time Apply runs unless something persists it: every page in
// an applied draft would register with no title, no slug, no SEO fields.
// Persisted here as JSON (not normalized into columns) since it's a small,
// single-file-scoped struct with no query needs of its own — see
// themefs.PageMeta.
//
// There is no apply_status CHECK constraint to widen on chat_messages:
// inspecting 20260727000001 and 20260727000002 (the brief's own
// instruction) shows chat_messages has exactly one CHECK constraint
// (chk_chat_messages_user_role, unrelated to apply_status) and no ENUM —
// apply_status is a plain VARCHAR(20), and 20260727000002's own doc comment
// already explains why: "Go's chat.Role/MessageStatus/ApplyStatus types are
// the actual constraint." "pending"/"discarded" need no schema change to
// become valid values there.
func Up_20260813000001(db *sql.DB) error {
	_, err := db.Exec(`
		ALTER TABLE chat_generated_files
		ADD COLUMN kind VARCHAR(20) NOT NULL DEFAULT 'proposed' AFTER action,
		ADD COLUMN page_meta JSON NULL AFTER previous_content,
		ADD INDEX idx_chat_generated_files_chat_message (chat_id, message_id)
	`)
	return err
}

func Down_20260813000001(db *sql.DB) error {
	_, err := db.Exec(`
		ALTER TABLE chat_generated_files
		DROP INDEX idx_chat_generated_files_chat_message,
		DROP COLUMN page_meta,
		DROP COLUMN kind
	`)
	return err
}

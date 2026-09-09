package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20260909000003_add_unique_key_to_chat_message_attachments",
		Up:   Up_20260909000003,
		Down: Down_20260909000003,
	})
}

// The ORDER BY message_id, kind, position tiebreak (see
// chat.Repository.listAttachmentMetadata/GetAttachmentsContent) makes reads
// deterministic, but nothing at the write layer stops two rows from
// actually sharing (message_id, kind, position) — a bug there would insert
// duplicates and reads would silently, arbitrarily pick one ordering or the
// other for them. This constraint makes that a write-time failure instead
// of a latent read-time ambiguity.
//
// Verified before writing this migration: no existing row (backfilled or
// otherwise) violates it —
//
//	SELECT message_id, kind, position, COUNT(*)
//	FROM chat_message_attachments
//	GROUP BY message_id, kind, position
//	HAVING COUNT(*) > 1;
//
// returned zero rows against the dev database's already-backfilled data.
func Up_20260909000003(db *sql.DB) error {
	_, err := db.Exec(`
		ALTER TABLE chat_message_attachments
		ADD UNIQUE KEY uq_cma_message_kind_position (message_id, kind, position)
	`)
	return err
}

func Down_20260909000003(db *sql.DB) error {
	_, err := db.Exec(`
		ALTER TABLE chat_message_attachments
		DROP INDEX uq_cma_message_kind_position
	`)
	return err
}

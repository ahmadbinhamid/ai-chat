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

// Prevents two data-loss bugs that only exist now that writes are deferred to a draft
// instead of applied immediately (see themebuild's package doc comment).
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

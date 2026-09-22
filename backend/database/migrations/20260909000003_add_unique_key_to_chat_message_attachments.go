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

// Adds a write-time constraint so a (message_id, kind, position) duplicate fails at insert
// instead of causing ambiguous read ordering downstream.
//
// Verified against the dev database before adding this: zero existing rows violated it.
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

package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20260908000001_add_image_to_chat_messages",
		Up:   Up_20260908000001,
		Down: Down_20260908000001,
	})
}

// images lets a user turn carry up to maxImagesPerMessage attachments, as a JSON array
// (LONGTEXT, not paired columns) since the count varies (1-5); NULL on non-image turns.
func Up_20260908000001(db *sql.DB) error {
	_, err := db.Exec(`
		ALTER TABLE chat_messages
		ADD COLUMN images LONGTEXT NULL AFTER created_at
	`)
	return err
}

func Down_20260908000001(db *sql.DB) error {
	_, err := db.Exec(`
		ALTER TABLE chat_messages
		DROP COLUMN images
	`)
	return err
}

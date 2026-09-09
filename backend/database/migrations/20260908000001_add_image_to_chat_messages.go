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

// images lets a user-role turn carry up to maxImagesPerMessage attached
// images (the image-attachment feature — attach design references, ask the
// AI to redesign a page/component against them). A single JSON-array
// column (LONGTEXT) rather than N sets of paired columns, since the count
// per message varies (1-5) — each array entry is
// {"base64": "...", "media_type": "..."}, same base64-string shape
// chat_generated_files.content already uses for images (see the
// 20260727000003 migration), just wrapped in a JSON array here instead of
// one bare string. NULL on every non-image turn, which is the vast
// majority.
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

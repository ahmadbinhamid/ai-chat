package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20261007000001_add_model_choice",
		Up:   Up_20261007000001,
		Down: Down_20261007000001,
	})
}

// model_id/effort: the catalogue model and effort a turn resolved to at enqueue (generations) and the one that
// answered (chat_messages). NULL on rows from before the catalogue, which use its default.
func Up_20261007000001(db *sql.DB) error {
	if _, err := db.Exec(`
		ALTER TABLE generations
		ADD COLUMN model_id VARCHAR(64) NULL AFTER mode,
		ADD COLUMN effort VARCHAR(16) NULL AFTER model_id
	`); err != nil {
		return err
	}
	_, err := db.Exec(`
		ALTER TABLE chat_messages
		ADD COLUMN model_id VARCHAR(64) NULL AFTER output_tokens,
		ADD COLUMN effort VARCHAR(16) NULL AFTER model_id
	`)
	return err
}

func Down_20261007000001(db *sql.DB) error {
	if _, err := db.Exec(`ALTER TABLE chat_messages DROP COLUMN effort, DROP COLUMN model_id`); err != nil {
		return err
	}
	_, err := db.Exec(`ALTER TABLE generations DROP COLUMN effort, DROP COLUMN model_id`)
	return err
}

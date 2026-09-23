package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20260812000001_add_generation_queue",
		Up:   Up_20260812000001,
		Down: Down_20260812000001,
	})
}

// Turns generations into a queue: any number of "queued" rows may wait behind at most one
func Up_20260812000001(db *sql.DB) error {
	if _, err := db.Exec(`
		ALTER TABLE generations
		ADD COLUMN prompt TEXT NOT NULL AFTER attempts,
		ADD COLUMN user_message_id CHAR(36) NULL AFTER prompt,
		ADD COLUMN theme_slug VARCHAR(255) NOT NULL DEFAULT '' AFTER user_message_id,
		ADD COLUMN mode VARCHAR(20) NOT NULL DEFAULT '' AFTER theme_slug,
		ADD COLUMN queued_at DATETIME NULL AFTER mode,
		MODIFY COLUMN started_at DATETIME NULL
	`); err != nil {
		return err
	}

	// Backs DequeueNext's per-chat ORDER BY (queued_at, id) lookup — avoids a table scan
	// as generations accumulate.
	_, err := db.Exec(`
		CREATE INDEX idx_generations_chat_status_queued ON generations (chat_id, status, queued_at)
	`)
	return err
}

func Down_20260812000001(db *sql.DB) error {
	if _, err := db.Exec(`DROP INDEX idx_generations_chat_status_queued ON generations`); err != nil {
		return err
	}
	_, err := db.Exec(`
		ALTER TABLE generations
		DROP COLUMN prompt,
		DROP COLUMN user_message_id,
		DROP COLUMN theme_slug,
		DROP COLUMN mode,
		DROP COLUMN queued_at,
		MODIFY COLUMN started_at DATETIME NOT NULL
	`)
	return err
}

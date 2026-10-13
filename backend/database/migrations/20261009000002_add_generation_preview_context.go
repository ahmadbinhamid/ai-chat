package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20261009000002_add_generation_preview_context",
		Up:   Up_20261009000002,
		Down: Down_20261009000002,
	})
}

// preview_route/focus_file: the page the merchant was looking at when sending, kept on the row so a queued or resumed
// generation still preloads it. preview_route NULL means unknown; "" is the home page.
func Up_20261009000002(db *sql.DB) error {
	_, err := db.Exec(`
		ALTER TABLE generations
		ADD COLUMN preview_route VARCHAR(512) NULL AFTER thinking_off,
		ADD COLUMN focus_file VARCHAR(512) NULL AFTER preview_route
	`)
	return err
}

func Down_20261009000002(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE generations DROP COLUMN focus_file, DROP COLUMN preview_route`)
	return err
}

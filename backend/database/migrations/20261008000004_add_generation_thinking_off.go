package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20261008000004_add_generation_thinking_off",
		Up:   Up_20261008000004,
		Down: Down_20261008000004,
	})
}

// thinking_off: Auto resolved this turn to its design route with thinking disabled (catalogue auto.design_thinking).
// Stored with model_id/effort so a queued turn runs exactly as resolved at enqueue.
func Up_20261008000004(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE generations ADD COLUMN thinking_off TINYINT(1) NOT NULL DEFAULT 0 AFTER effort`)
	return err
}

func Down_20261008000004(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE generations DROP COLUMN thinking_off`)
	return err
}

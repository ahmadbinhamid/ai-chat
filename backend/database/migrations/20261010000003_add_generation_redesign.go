package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20261010000003_add_generation_redesign",
		Up:   Up_20261010000003,
		Down: Down_20261010000003,
	})
}

// redesign: decided from the prompt at enqueue, so a queued turn runs with the same route and brief it was routed for.
func Up_20261010000003(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE generations ADD COLUMN redesign TINYINT(1) NOT NULL DEFAULT 0 AFTER auto_selected`)
	return err
}

func Down_20261010000003(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE generations DROP COLUMN redesign`)
	return err
}

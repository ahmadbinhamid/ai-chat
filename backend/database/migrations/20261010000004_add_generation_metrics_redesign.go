package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20261010000004_add_generation_metrics_redesign",
		Up:   Up_20261010000004,
		Down: Down_20261010000004,
	})
}

// redesign: the turn ran as a redesign, so redesign turns can be compared apart from ordinary edits.
func Up_20261010000004(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE generation_metrics ADD COLUMN redesign TINYINT(1) NOT NULL DEFAULT 0 AFTER escalated`)
	return err
}

func Down_20261010000004(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE generation_metrics DROP COLUMN redesign`)
	return err
}

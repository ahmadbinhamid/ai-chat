package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20261010000002_add_generation_metrics_escalated",
		Up:   Up_20261010000002,
		Down: Down_20261010000002,
	})
}

// escalated: Auto's design model got stuck and the turn was retried on Auto's fix model; model_id is then the fix model.
func Up_20261010000002(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE generation_metrics ADD COLUMN escalated TINYINT(1) NOT NULL DEFAULT 0 AFTER effort`)
	return err
}

func Down_20261010000002(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE generation_metrics DROP COLUMN escalated`)
	return err
}

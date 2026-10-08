package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20261008000002_add_generation_resume",
		Up:   Up_20261008000002,
		Down: Down_20261008000002,
	})
}

// resume_count: times a drain re-queued this generation (at most once). awaiting_resume_since: when a restart left it
// queued without its sender's bearer token, which lives only in memory; it runs once that user is back.
func Up_20261008000002(db *sql.DB) error {
	_, err := db.Exec(`
		ALTER TABLE generations
		ADD COLUMN resume_count INT NOT NULL DEFAULT 0 AFTER attempts,
		ADD COLUMN awaiting_resume_since DATETIME(6) NULL AFTER resume_count
	`)
	return err
}

func Down_20261008000002(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE generations DROP COLUMN awaiting_resume_since, DROP COLUMN resume_count`)
	return err
}

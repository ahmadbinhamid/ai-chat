package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20261009000003_add_generation_metrics_preload",
		Up:   Up_20261009000003,
		Down: Down_20261009000003,
	})
}

// preloaded_files/preloaded_bytes: theme files sent with the first prompt, to compare against tool_calls.
func Up_20261009000003(db *sql.DB) error {
	_, err := db.Exec(`
		ALTER TABLE generation_metrics
		ADD COLUMN preloaded_files INT UNSIGNED NOT NULL DEFAULT 0 AFTER tool_calls,
		ADD COLUMN preloaded_bytes BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER preloaded_files
	`)
	return err
}

func Down_20261009000003(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE generation_metrics DROP COLUMN preloaded_bytes, DROP COLUMN preloaded_files`)
	return err
}

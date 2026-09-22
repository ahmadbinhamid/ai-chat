package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20260813000002_add_generation_heartbeat",
		Up:   Up_20260813000002,
		Down: Down_20260813000002,
	})
}

// last_heartbeat_at decouples ReapStaleGenerations' staleness check from the 65-minute
// generateTimeout, so a stuck generation isn't left "running" for the full budget before being reaped.
//
// Stamped on every progress event (tool calls, deltas, retries); NULL for existing/never-updated
// rows is deliberate — the reaper falls back to started_at.
func Up_20260813000002(db *sql.DB) error {
	_, err := db.Exec(`
		ALTER TABLE generations
		ADD COLUMN last_heartbeat_at DATETIME NULL AFTER started_at,
		ADD INDEX idx_generations_status_heartbeat (status, last_heartbeat_at)
	`)
	return err
}

func Down_20260813000002(db *sql.DB) error {
	_, err := db.Exec(`
		ALTER TABLE generations
		DROP INDEX idx_generations_status_heartbeat,
		DROP COLUMN last_heartbeat_at
	`)
	return err
}

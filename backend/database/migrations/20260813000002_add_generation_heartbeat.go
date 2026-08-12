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

// last_heartbeat_at decouples ReapStaleGenerations' staleness check from
// generateTimeout (the 65-minute per-generation budget — see service.go).
// Before this, a genuinely stuck generation stayed marked "running" for up
// to that entire 65 minutes before the reaper would touch it, because the
// two concerns — "how long may a healthy generation legitimately run" and
// "how long has this one gone without any sign of life" — shared one
// number. eventEmitter.emit already runs on every progress event a healthy
// generation produces (tool calls, streamed deltas' periodic checkpoints,
// checkAndRepair retries), so stamping it there gives the reaper a much
// tighter, heartbeat-based signal without adding a new write path. NULL
// (not defaulted to NOW()) for existing/never-updated rows is deliberate:
// ReapStaleGenerations falls back to started_at for those, exactly as it
// did before this column existed.
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

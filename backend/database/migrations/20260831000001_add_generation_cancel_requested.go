package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20260831000001_add_generation_cancel_requested",
		Up:   Up_20260831000001,
		Down: Down_20260831000001,
	})
}

// cancel_requested_at is the durable half of cancelling a running generation — the live
// EventTypeCancelRequested signal is best-effort and can be missed or dropped under load.
//
// runOneQueuedGeneration checks this column on subscribe and on every heartbeat tick as a
// backstop, so a cancel request is never silently lost.
func Up_20260831000001(db *sql.DB) error {
	_, err := db.Exec(`
		ALTER TABLE generations
		ADD COLUMN cancel_requested_at DATETIME NULL AFTER last_heartbeat_at
	`)
	return err
}

func Down_20260831000001(db *sql.DB) error {
	_, err := db.Exec(`
		ALTER TABLE generations
		DROP COLUMN cancel_requested_at
	`)
	return err
}

package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20261008000001_widen_last_heartbeat_at",
		Up:   Up_20261008000001,
		Down: Down_20261008000001,
	})
}

// Second precision dropped or rounded each heartbeat's fraction, so one stamped during a call could read as earlier
// than the call itself. Same widening 20260922000001 gave queued_at.
func Up_20261008000001(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE generations MODIFY COLUMN last_heartbeat_at DATETIME(6) NULL`)
	return err
}

func Down_20261008000001(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE generations MODIFY COLUMN last_heartbeat_at DATETIME NULL`)
	return err
}

package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20260922000001_widen_ordering_datetime_columns",
		Up:   Up_20260922000001,
		Down: Down_20260922000001,
	})
}

// Widens queued_at/created_at columns to DATETIME(6) so same-second writes sort by real
// insertion order instead of an id/row-visit tiebreak.
func Up_20260922000001(db *sql.DB) error {
	if _, err := db.Exec(`ALTER TABLE generations MODIFY COLUMN queued_at DATETIME(6) NULL`); err != nil {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE chat_messages MODIFY COLUMN created_at DATETIME(6) NOT NULL`); err != nil {
		return err
	}
	_, err := db.Exec(`ALTER TABLE chat_generated_files MODIFY COLUMN created_at DATETIME(6) NOT NULL`)
	return err
}

func Down_20260922000001(db *sql.DB) error {
	if _, err := db.Exec(`ALTER TABLE generations MODIFY COLUMN queued_at DATETIME NULL`); err != nil {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE chat_messages MODIFY COLUMN created_at DATETIME NOT NULL`); err != nil {
		return err
	}
	_, err := db.Exec(`ALTER TABLE chat_generated_files MODIFY COLUMN created_at DATETIME NOT NULL`)
	return err
}

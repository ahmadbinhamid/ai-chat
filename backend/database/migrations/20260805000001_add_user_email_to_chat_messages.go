package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20260805000001_add_user_email_to_chat_messages",
		Up:   Up_20260805000001,
		Down: Down_20260805000001,
	})
}

// user_email mirrors user_name — same nullable-for-non-user-turns shape (see
// chk_chat_messages_user_role), sourced from auth.Identity.Email so the
// transcript can attribute a turn beyond just a display name.
func Up_20260805000001(db *sql.DB) error {
	_, err := db.Exec(`
		ALTER TABLE chat_messages
		ADD COLUMN user_email VARCHAR(255) NULL AFTER user_name
	`)
	return err
}

func Down_20260805000001(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE chat_messages DROP COLUMN user_email`)
	return err
}

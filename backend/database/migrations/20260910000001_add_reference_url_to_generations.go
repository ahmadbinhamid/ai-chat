package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20260910000001_add_reference_url_to_generations",
		Up:   Up_20260910000001,
		Down: Down_20260910000001,
	})
}

// reference_url carries a prompt URL through the queue so the fetch happens in doGenerate
// (background) instead of blocking POST /chats/messages on outbound network I/O.
//
// 2048 matches urlfetch.maxURLLen; NULL when no URL was found or an HTML upload took precedence.
func Up_20260910000001(db *sql.DB) error {
	_, err := db.Exec(`
		ALTER TABLE generations
		ADD COLUMN reference_url VARCHAR(2048) NULL AFTER prompt
	`)
	return err
}

func Down_20260910000001(db *sql.DB) error {
	_, err := db.Exec(`
		ALTER TABLE generations
		DROP COLUMN reference_url
	`)
	return err
}

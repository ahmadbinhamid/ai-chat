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

// reference_url carries a URL found in the merchant's prompt through the
// queue, so the actual fetch can happen in doGenerate (where slow work
// belongs — see cmd/server/main.go's own "no route does slow synchronous
// work" invariant) instead of blocking Service.Generate/POST
// /chats/messages on an outbound network call. NULL for every row where no
// URL was found (the overwhelming majority) or where an explicit HTML
// upload already took precedence — see Service.Generate. 2048 matches
// urlfetch.maxURLLen, the longest URL ValidateURL will ever accept.
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

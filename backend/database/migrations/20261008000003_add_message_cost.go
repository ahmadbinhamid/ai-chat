package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20261008000003_add_message_cost",
		Up:   Up_20261008000003,
		Down: Down_20261008000003,
	})
}

// cost_usd: what the provider charged for the reply's calls, next to model_id for per-model cost reports. NULL when
// the provider doesn't report cost (direct DeepSeek) and on rows from before it was recorded. Calls can cost under a
// millionth of a dollar, hence 10 decimal places.
func Up_20261008000003(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE chat_messages ADD COLUMN cost_usd DECIMAL(20,10) NULL AFTER effort`)
	return err
}

func Down_20261008000003(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE chat_messages DROP COLUMN cost_usd`)
	return err
}

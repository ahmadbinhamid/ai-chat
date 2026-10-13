package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20261010000001_add_generation_auto_selected",
		Up:   Up_20261010000001,
		Down: Down_20261010000001,
	})
}

// auto_selected: the merchant chose Auto, so a stuck design-route turn may escalate to Auto's fix model. model_id holds
// only the resolved concrete model, which can't tell Auto's Flash from Flash picked by hand.
func Up_20261010000001(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE generations ADD COLUMN auto_selected TINYINT(1) NOT NULL DEFAULT 0 AFTER thinking_off`)
	return err
}

func Down_20261010000001(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE generations DROP COLUMN auto_selected`)
	return err
}

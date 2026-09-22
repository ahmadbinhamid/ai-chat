package migrations

import (
	"database/sql"
	"fmt"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20260909000005_add_updated_at_everywhere",
		Up:   Up_20260909000005,
		Down: Down_20260909000005,
	})
}

// chat_messages, generation_events and chat_message_attachments get updated_at for schema
// consistency, even though rows stay immutable — set once at insert, equal to created_at.
//
// generations gets real created_at/updated_at (it had neither) — unlike the other three it's
// genuinely mutated many times, so updated_at is actively bumped, not write-once.
//
// created_at is backfilled from the earliest existing timestamp per row (queued_at, then
// started_at, then finished_at, then now as a last resort).
//
// Each ALTER runs as add-nullable, backfill, then make-NOT-NULL: a NOT NULL column with no
// default can't be added directly to a table that already has rows.
func Up_20260909000005(db *sql.DB) error {
	simple := []string{"chat_messages", "generation_events", "chat_message_attachments"}
	for _, table := range simple {
		if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN updated_at DATETIME NULL AFTER created_at`, table)); err != nil {
			return fmt.Errorf("add updated_at to %s: %w", table, err)
		}
		if _, err := db.Exec(fmt.Sprintf(`UPDATE %s SET updated_at = created_at WHERE updated_at IS NULL`, table)); err != nil {
			return fmt.Errorf("backfill updated_at on %s: %w", table, err)
		}
		if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE %s MODIFY COLUMN updated_at DATETIME NOT NULL`, table)); err != nil {
			return fmt.Errorf("finalize updated_at on %s: %w", table, err)
		}
	}

	if _, err := db.Exec(`
		ALTER TABLE generations
		ADD COLUMN created_at DATETIME NULL AFTER finished_at,
		ADD COLUMN updated_at DATETIME NULL AFTER created_at
	`); err != nil {
		return fmt.Errorf("add created_at/updated_at to generations: %w", err)
	}
	if _, err := db.Exec(`
		UPDATE generations
		SET created_at = COALESCE(queued_at, started_at, finished_at, NOW()),
		    updated_at = COALESCE(finished_at, last_heartbeat_at, cancel_requested_at, started_at, queued_at, NOW())
		WHERE created_at IS NULL OR updated_at IS NULL
	`); err != nil {
		return fmt.Errorf("backfill created_at/updated_at on generations: %w", err)
	}
	if _, err := db.Exec(`
		ALTER TABLE generations
		MODIFY COLUMN created_at DATETIME NOT NULL,
		MODIFY COLUMN updated_at DATETIME NOT NULL
	`); err != nil {
		return fmt.Errorf("finalize created_at/updated_at on generations: %w", err)
	}

	return nil
}

func Down_20260909000005(db *sql.DB) error {
	simple := []string{"chat_messages", "generation_events", "chat_message_attachments"}
	for _, table := range simple {
		if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE %s DROP COLUMN updated_at`, table)); err != nil {
			return fmt.Errorf("drop updated_at from %s: %w", table, err)
		}
	}
	_, err := db.Exec(`
		ALTER TABLE generations
		DROP COLUMN created_at,
		DROP COLUMN updated_at
	`)
	if err != nil {
		return fmt.Errorf("drop created_at/updated_at from generations: %w", err)
	}
	return nil
}

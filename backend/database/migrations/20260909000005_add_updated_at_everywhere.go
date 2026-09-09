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

// chat_messages, generation_events and chat_message_attachments were all
// created without updated_at, deliberately — each is (or was believed to
// be) append-only, and the prior convention in this codebase was that a
// missing updated_at signals "this row is never mutated after insert." That
// convention is retired as of this migration: every table gets both
// created_at and updated_at from now on, appended-only or not, for schema
// consistency — not because any of these three tables has started being
// mutated. Each one's rows are still exactly as immutable as before;
// updated_at is simply set once, at insert, to the same value as
// created_at, and never touched again (see the Go write-path changes
// alongside this migration — chat.createMessage/createAttachments,
// themebuild's generation_events.go).
//
// generations gets both created_at AND updated_at added — it had neither.
// Unlike the other three, generations genuinely is mutated many times over
// its life (status transitions, heartbeats, cancellation, completion — see
// repository_generation.go's many UPDATE generations statements), so
// updated_at here is a REAL "last touched" timestamp, actively bumped on
// every one of those UPDATEs going forward — not a write-once column like
// the other three. created_at is backfilled from the earliest existing
// timestamp each row already has (queued_at, falling back to started_at,
// finished_at, or now if a row somehow has none — shouldn't happen, but a
// NOT NULL column needs a value for every existing row regardless).
// generations already has its own richer, purpose-built timestamps
// (queued_at/started_at/last_heartbeat_at/cancel_requested_at/finished_at)
// for precise lifecycle tracking — created_at/updated_at sit alongside
// those, not instead of them, matching every other table's shape rather
// than being redundant with the specific ones.
//
// Each ALTER runs as add-nullable -> backfill -> make-NOT-NULL, in three
// steps per table, because a NOT NULL column with no default can't be added
// directly to a table that already has rows (this migration runs against
// databases with real data in all four tables).
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

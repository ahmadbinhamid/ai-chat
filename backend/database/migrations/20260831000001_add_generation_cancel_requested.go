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

// cancel_requested_at is the durable half of cancelling a *running*
// generation (see themebuild.Service.CancelQueuedGeneration's running
// branch). The live EventTypeCancelRequested signal published alongside it
// is best-effort and can miss its listener entirely: either published in
// the gap between DequeueNext marking a row running and
// runOneQueuedGeneration's own goroutine reaching its Subscribe call, or
// dropped from a saturated live-event buffer under load (it shares that
// channel with high-frequency "thinking" deltas — see eventBus's
// subscriberBufferSize). This column is what a late or dropped signal is
// recovered from: runOneQueuedGeneration checks it once immediately after
// subscribing, then again on every heartbeat-ticker tick as a backstop, so
// a cancel request is never silently lost — just possibly a little slower
// than the live path.
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

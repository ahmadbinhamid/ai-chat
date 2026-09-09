package migrations

import (
	"database/sql"
	"fmt"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20260909000002_create_chat_message_attachments",
		Up:   Up_20260909000002,
		Down: Down_20260909000002,
	})
}

// chat_message_attachments replaces chat_messages' per-type attachment
// columns (images, html_attachment_filename/content — see the 20260908000001
// and 20260909000001 migrations) with one polymorphic table, for two
// reasons: adding a new attachment type no longer costs a migration plus new
// Go fields/scan args/validation constants (it costs one `kind` value and a
// themebuild validation-limit entry), and — the bigger one — a transcript
// read (ListMessagesByChat, which runs on every GET /chat) no longer has to
// pull every attachment's bytes off the row just to report a message
// happened. Bytes still live in MySQL (external object storage was
// considered and rejected — this service runs multi-replica with no shared
// volume, and standing up S3 isn't justified at current volume); what moves
// is WHICH read pays for them: only doGenerate, immediately before it
// builds a model input from a specific message's attachments, not every
// transcript load. See chat.MessageAttachment's own doc comment for the
// read-path split.
//
// content LONGBLOB, not LONGTEXT — deliberate, even for kind='html' (text,
// but opaque here: never SQL-searched, only ever fed to the model or
// decoded back to a string at the one read site that needs it). One column
// type keeps the table genuinely polymorphic; media_type says how to
// interpret the bytes. Raw decoded bytes, not base64: base64 exists to
// carry binary through a text channel like a JSON request body, which is
// exactly where it stays (chat.MessageImage, the wire/write-path DTO) —
// decoded once on the way in, re-encoded transiently only when assembling
// an Anthropic/DeepSeek request body, never stored that way.
//
// storage_key is reserved and unused today (always NULL; content is always
// populated) — it exists so moving bytes to external storage later is a
// config change plus a copy job, not a schema redesign. Exactly one of
// content/storage_key is meaningful for a given row.
//
// checksum is sha256 of the raw bytes, tenant-scoped indexed (integrity now;
// dedupe later — recorded, not acted on: two rows can share a checksum and
// each still owns its own bytes, so deleting one can never strip another's
// content out from under it).
//
// No updated_at: unlike chat_generated_files (whose rows transition
// pending -> applied/discarded after insert, see the 20260813000001
// migration), a row here is never mutated after creation — only inserted,
// and removed automatically via ON DELETE CASCADE when its message's chat
// is deleted. That cascade is a real advantage over external storage: no
// orphan-blob sweeper needed, the database reclaims the bytes itself.
//
// Old columns (images, html_attachment_filename/content) are deliberately
// NOT dropped here — see the 20260909000004 migration, which drops all
// three in a separate step.
//
// No backfill: this migration originally included one (unnest the old
// images JSON array via JSON_TABLE/FROM_BASE64, copy the single
// html_attachment_* pair), written when this feature was believed to have
// live data in those columns somewhere it needed to preserve. It never did
// — this whole feature has only ever run against one developer's local
// database, and confirmed before removing the backfill: production has
// never had this feature deployed, so the old columns hold nothing there
// either. Removed rather than left as dead-but-harmless code: a backfill
// that never runs against real data is still a real maintenance/review
// burden (JSON_TABLE/FROM_BASE64 correctness, filename-derivation rules)
// for a codepath nothing will ever execute meaningfully. If a real backfill
// need ever resurfaces, write a fresh migration for it rather than
// resurrecting this one — by then the old columns may not even exist
// anymore (see the 20260909000004 migration).
func Up_20260909000002(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS chat_message_attachments (
		  id           CHAR(36)        NOT NULL PRIMARY KEY,
		  message_id   CHAR(36)        NOT NULL,
		  tenant_id    BIGINT UNSIGNED NOT NULL,
		  kind         VARCHAR(32)     NOT NULL,
		  filename     VARCHAR(255)    NOT NULL,
		  media_type   VARCHAR(127)    NOT NULL,
		  size_bytes   BIGINT UNSIGNED NOT NULL,
		  checksum     CHAR(64)        NOT NULL,
		  position     INT UNSIGNED    NOT NULL DEFAULT 0,
		  content      LONGBLOB        NULL,
		  storage_key  VARCHAR(512)    NULL,
		  created_at   DATETIME        NOT NULL,
		  CONSTRAINT fk_cma_message FOREIGN KEY (message_id) REFERENCES chat_messages (id) ON DELETE CASCADE,
		  INDEX idx_cma_message_position (message_id, position),
		  INDEX idx_cma_tenant_checksum (tenant_id, checksum)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
	`)
	if err != nil {
		return fmt.Errorf("create chat_message_attachments: %w", err)
	}
	return nil
}

func Down_20260909000002(db *sql.DB) error {
	_, err := db.Exec(`DROP TABLE IF EXISTS chat_message_attachments`)
	return err
}

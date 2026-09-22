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

// chat_message_attachments replaces chat_messages' per-type attachment columns (images,
// html_attachment_filename/content) with one polymorphic table — a new type costs no migration.
//
// Transcript reads (every GET /chat) no longer pull attachment bytes; only doGenerate reads content.
//
// Bytes stay in MySQL, not external storage — multi-replica with no shared volume, and not
// justified at current volume; see chat.MessageAttachment's doc comment for the read split.
//
// content is LONGBLOB (not LONGTEXT, even for kind='html') to keep the table polymorphic;
// stored as raw decoded bytes, not base64 — base64 is used only on the wire/request path.
//
// storage_key is reserved for future external storage (always NULL today); exactly one of
// content/storage_key is meaningful per row.
//
// checksum is sha256 of the raw bytes, tenant-scoped and indexed — recorded for future dedupe,
// not enforced; two rows may share one and each still owns its own bytes independently.
//
// No updated_at: rows are insert-only, removed via ON DELETE CASCADE when the message's chat
// is deleted — no orphan-blob sweeper needed.
//
// Old columns (images, html_attachment_filename/content) are intentionally left in place here
// — dropped separately in 20260909000004.
//
// No backfill: this feature has never run against real data (dev-only, never deployed), so
// there's nothing to preserve. If that's no longer true, write a fresh backfill migration first.
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

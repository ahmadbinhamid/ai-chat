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

// Polymorphic attachments table, insert-only. Old columns dropped separately in 20260909000004.
// No updated_at, no backfill (feature dev-only, never deployed).
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

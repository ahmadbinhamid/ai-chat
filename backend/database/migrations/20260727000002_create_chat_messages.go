package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20260727000002_create_chat_messages",
		Up:   Up_20260727000002,
		Down: Down_20260727000002,
	})
}

// chat_messages is the append-only turn log of a chat — no updated_at: once
// written, a message is never mutated, only superseded by a later row.
// tenant_id is denormalized from chats so a message row is self-sufficient
// for tenant-scoped checks without a join. role/status/apply_status are
// plain VARCHAR rather than ENUM so a new value never needs a schema
// migration — Go's chat.Role/MessageStatus/ApplyStatus types are the actual
// constraint. apply_status is set directly to applied/not_applicable at
// generation time now (see themebuild.Service.Generate) — there is no
// pending state to wait on, generation writes to the real theme immediately.
func Up_20260727000002(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS chat_messages (
		  id             CHAR(36)        NOT NULL PRIMARY KEY,
		  chat_id        CHAR(36)        NOT NULL,
		  tenant_id      BIGINT UNSIGNED NOT NULL,
		  role           VARCHAR(20)     NOT NULL,
		  user_id        BIGINT UNSIGNED NULL,
		  user_name      VARCHAR(255)    NULL,
		  content        LONGTEXT        NOT NULL,
		  status         VARCHAR(20)     NOT NULL DEFAULT 'completed',
		  error_message  TEXT            NULL,
		  input_tokens   INT UNSIGNED    NOT NULL DEFAULT 0,
		  output_tokens  INT UNSIGNED    NOT NULL DEFAULT 0,
		  apply_status   VARCHAR(20)     NOT NULL DEFAULT 'not_applicable',
		  applied_at     DATETIME        NULL,
		  created_at     DATETIME        NOT NULL,
		  CONSTRAINT fk_chat_messages_chat FOREIGN KEY (chat_id) REFERENCES chats (id) ON DELETE CASCADE,
		  CONSTRAINT chk_chat_messages_user_role CHECK (role <> 'user' OR user_id IS NOT NULL),
		  INDEX idx_chat_messages_chat_created (chat_id, created_at)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
	`)
	return err
}

func Down_20260727000002(db *sql.DB) error {
	_, err := db.Exec(`DROP TABLE IF EXISTS chat_messages`)
	return err
}

package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20260730000001_create_generations",
		Up:   Up_20260730000001,
		Down: Down_20260730000001,
	})
}

// generations is a durable, multi-replica-safe replacement for the old in-memory
func Up_20260730000001(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS generations (
		  id              CHAR(36)        NOT NULL PRIMARY KEY,
		  chat_id         CHAR(36)        NOT NULL,
		  tenant_id       BIGINT UNSIGNED NOT NULL,
		  status          VARCHAR(20)     NOT NULL DEFAULT 'running',
		  error           TEXT            NULL,
		  attempts        INT UNSIGNED    NOT NULL DEFAULT 0,
		  started_at      DATETIME        NOT NULL,
		  finished_at     DATETIME        NULL,
		  running_chat_id CHAR(36) AS (CASE WHEN status = 'running' THEN chat_id ELSE NULL END) VIRTUAL,
		  UNIQUE KEY uniq_generations_running_chat (running_chat_id),
		  INDEX idx_generations_chat_started (chat_id, started_at),
		  INDEX idx_generations_status_started (status, started_at)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
	`)
	return err
}

func Down_20260730000001(db *sql.DB) error {
	_, err := db.Exec(`DROP TABLE IF EXISTS generations`)
	return err
}

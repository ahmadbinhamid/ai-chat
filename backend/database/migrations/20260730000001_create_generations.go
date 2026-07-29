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

// generations is the durable replacement for themebuild's in-memory
// generationTracker (phase 3a) — a row per background Generate call, so
// GET /chat's generating/generation_error fields survive a pod restart and
// work correctly with more than one replica, and so a reaper can find and
// fail generations whose process died mid-run.
//
// MySQL has no native "unique index only when a condition holds", unlike
// Postgres' partial unique index — running_chat_id is the standard
// workaround: a virtual column that's NULL except while status='running',
// with a plain UNIQUE key on it. MySQL unique indexes allow any number of
// NULLs, so only the "one running generation per chat" case is actually
// constrained; a chat's many succeeded/failed rows never collide.
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

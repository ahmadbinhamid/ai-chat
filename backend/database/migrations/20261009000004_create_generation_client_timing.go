package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20261009000004_create_generation_client_timing",
		Up:   Up_20261009000004,
		Down: Down_20261009000004,
	})
}

// generation_client_timing: the browser's own timings for a generation, ms after the merchant pressed send. Separate
// from generation_metrics because the browser's report can arrive before or after that row is written.
func Up_20261009000004(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS generation_client_timing (
		  generation_id      CHAR(36)        NOT NULL PRIMARY KEY,
		  chat_id            CHAR(36)        NOT NULL,
		  tenant_id          BIGINT UNSIGNED NOT NULL,
		  post_ms            INT UNSIGNED    NULL,
		  ws_connect_ms      INT UNSIGNED    NULL,
		  first_event_ms     INT UNSIGNED    NULL,
		  first_progress_ms  INT UNSIGNED    NULL,
		  completed_ms       INT UNSIGNED    NULL,
		  preview_visible_ms INT UNSIGNED    NULL,
		  created_at         DATETIME        NOT NULL,
		  updated_at         DATETIME        NOT NULL,
		  INDEX idx_generation_client_timing_tenant_created (tenant_id, created_at)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
	`)
	return err
}

func Down_20261009000004(db *sql.DB) error {
	_, err := db.Exec(`DROP TABLE IF EXISTS generation_client_timing`)
	return err
}

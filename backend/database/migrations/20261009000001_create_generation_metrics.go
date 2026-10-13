package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20261009000001_create_generation_metrics",
		Up:   Up_20261009000001,
		Down: Down_20261009000001,
	})
}

// generation_metrics: one row per finished generation for latency/cost percentiles (see docs/metrics.sql). total_ms
// is dequeue to end, excluding queue_wait_ms; model/tool/token columns cover model calls that returned, 0 if none did.
func Up_20261009000001(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS generation_metrics (
		  generation_id     CHAR(36)        NOT NULL PRIMARY KEY,
		  chat_id           CHAR(36)        NOT NULL,
		  tenant_id         BIGINT UNSIGNED NOT NULL,
		  model_id          VARCHAR(64)     NULL,
		  effort            VARCHAR(16)     NULL,
		  outcome           ENUM('succeeded','failed','cancelled') NOT NULL,
		  queue_wait_ms     BIGINT UNSIGNED NOT NULL DEFAULT 0,
		  context_build_ms  BIGINT UNSIGNED NOT NULL DEFAULT 0,
		  first_token_ms    BIGINT UNSIGNED NOT NULL DEFAULT 0,
		  model_ms          BIGINT UNSIGNED NOT NULL DEFAULT 0,
		  tool_ms           BIGINT UNSIGNED NOT NULL DEFAULT 0,
		  tool_calls        INT UNSIGNED    NOT NULL DEFAULT 0,
		  iterations        INT UNSIGNED    NOT NULL DEFAULT 0,
		  validation_ms     BIGINT UNSIGNED NOT NULL DEFAULT 0,
		  repair_attempts   INT UNSIGNED    NOT NULL DEFAULT 0,
		  repair_ms         BIGINT UNSIGNED NOT NULL DEFAULT 0,
		  input_tokens      BIGINT UNSIGNED NOT NULL DEFAULT 0,
		  output_tokens     BIGINT UNSIGNED NOT NULL DEFAULT 0,
		  cache_read_tokens BIGINT UNSIGNED NOT NULL DEFAULT 0,
		  cost_usd          DECIMAL(20,10)  NULL,
		  total_ms          BIGINT UNSIGNED NOT NULL DEFAULT 0,
		  created_at        DATETIME        NOT NULL,
		  updated_at        DATETIME        NOT NULL,
		  INDEX idx_generation_metrics_tenant_created (tenant_id, created_at),
		  INDEX idx_generation_metrics_model_created (model_id, created_at)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
	`)
	return err
}

func Down_20261009000001(db *sql.DB) error {
	_, err := db.Exec(`DROP TABLE IF EXISTS generation_metrics`)
	return err
}

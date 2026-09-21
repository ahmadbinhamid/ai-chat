package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20260918000001_create_builder_execution_examples",
		Up:   Up_20260918000001,
		Down: Down_20260918000001,
	})
}

// builder_execution_examples stores compact, tenant-scoped BuilderExecution
// training/evaluation records (ML-9). Payload is JSON only — no raw prompts
// by default, no provider bodies, no secrets. Retention is enforced by the
// collector (days + max rows), not unbounded growth.
func Up_20260918000001(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS builder_execution_examples (
		  id                 CHAR(36)        NOT NULL PRIMARY KEY,
		  tenant_id          BIGINT UNSIGNED NOT NULL,
		  chat_id            CHAR(36)        NULL,
		  generation_id      CHAR(36)        NULL,
		  prompt_fingerprint CHAR(64)        NOT NULL,
		  outcome_category   VARCHAR(40)     NOT NULL,
		  training_positive  TINYINT(1)      NOT NULL DEFAULT 0,
		  success            TINYINT(1)      NOT NULL DEFAULT 0,
		  payload            JSON            NOT NULL,
		  created_at         DATETIME        NOT NULL,
		  updated_at         DATETIME        NOT NULL,
		  INDEX idx_builder_examples_tenant_created (tenant_id, created_at),
		  INDEX idx_builder_examples_tenant_outcome (tenant_id, outcome_category),
		  INDEX idx_builder_examples_created (created_at)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
	`)
	return err
}

func Down_20260918000001(db *sql.DB) error {
	_, err := db.Exec(`DROP TABLE IF EXISTS builder_execution_examples`)
	return err
}

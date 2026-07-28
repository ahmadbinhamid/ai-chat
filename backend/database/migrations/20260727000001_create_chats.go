package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20260727000001_create_chats",
		Up:   Up_20260727000001,
		Down: Down_20260727000001,
	})
}

// chats is the one, ongoing conversation thread per (tenant_id, type) — no
// per-user ownership and no theme_slug: this table is deliberately generic
// (see the chat package's doc comment) so it isn't coupled to the
// theme-builder use case. type is "builder" today; a future chat use case
// on the same tenant gets its own row via a new type value, not a schema
// change. There's no local FK to tenant/user: identity lives in the calling
// system, not duplicated here (see internal/server/handlers/identity.go —
// this service trusts the identity headers a trusted caller sets).
func Up_20260727000001(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS chats (
		  id                   CHAR(36)        NOT NULL PRIMARY KEY,
		  tenant_id            BIGINT UNSIGNED NOT NULL,
		  type                 VARCHAR(255)    NOT NULL,
		  total_input_tokens   BIGINT UNSIGNED NOT NULL DEFAULT 0,
		  total_output_tokens  BIGINT UNSIGNED NOT NULL DEFAULT 0,
		  last_message_at      DATETIME        NULL,
		  created_at           DATETIME        NOT NULL,
		  updated_at           DATETIME        NOT NULL,
		  UNIQUE KEY uniq_chats_tenant_type (tenant_id, type),
		  INDEX idx_chats_tenant_last_message (tenant_id, last_message_at)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
	`)
	return err
}

func Down_20260727000001(db *sql.DB) error {
	_, err := db.Exec(`DROP TABLE IF EXISTS chats`)
	return err
}

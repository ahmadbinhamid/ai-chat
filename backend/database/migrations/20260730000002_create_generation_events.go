package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20260730000002_create_generation_events",
		Up:   Up_20260730000002,
		Down: Down_20260730000002,
	})
}

// generation_events is the durable event log a WebSocket client replays on reconnect.
func Up_20260730000002(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS generation_events (
		  id            CHAR(36)        NOT NULL PRIMARY KEY,
		  generation_id CHAR(36)        NOT NULL,
		  chat_id       CHAR(36)        NOT NULL,
		  seq           BIGINT UNSIGNED NOT NULL,
		  type          VARCHAR(50)     NOT NULL,
		  payload       JSON            NULL,
		  created_at    DATETIME        NOT NULL,
		  CONSTRAINT fk_generation_events_generation FOREIGN KEY (generation_id) REFERENCES generations (id) ON DELETE CASCADE,
		  UNIQUE KEY uniq_generation_events_generation_seq (generation_id, seq),
		  INDEX idx_generation_events_chat_seq (chat_id, seq)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
	`)
	return err
}

func Down_20260730000002(db *sql.DB) error {
	_, err := db.Exec(`DROP TABLE IF EXISTS generation_events`)
	return err
}

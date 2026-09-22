package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20260727000003_create_chat_generated_files",
		Up:   Up_20260727000003,
		Down: Down_20260727000003,
	})
}

// chat_generated_files records every file an assistant message changed; file_path is
// required since it's the only record of where content was written.
//
// chat_id is denormalized from message_id to avoid a join; pending/applied/discarded
// tracking is added by the 20260813000001 migration.
func Up_20260727000003(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS chat_generated_files (
		  id                CHAR(36)        NOT NULL PRIMARY KEY,
		  message_id        CHAR(36)        NOT NULL,
		  chat_id           CHAR(36)        NOT NULL,
		  file_path         VARCHAR(500)    NOT NULL,
		  action            VARCHAR(20)     NOT NULL,
		  language          VARCHAR(20)     NOT NULL DEFAULT '',
		  content           LONGTEXT        NOT NULL,
		  previous_content  LONGTEXT        NULL,
		  created_at        DATETIME        NOT NULL,
		  updated_at        DATETIME        NOT NULL,
		  CONSTRAINT fk_chat_generated_files_message FOREIGN KEY (message_id) REFERENCES chat_messages (id) ON DELETE CASCADE,
		  CONSTRAINT fk_chat_generated_files_chat FOREIGN KEY (chat_id) REFERENCES chats (id) ON DELETE CASCADE,
		  UNIQUE KEY uq_chat_generated_files_message_path (message_id, file_path)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
	`)
	return err
}

func Down_20260727000003(db *sql.DB) error {
	_, err := db.Exec(`DROP TABLE IF EXISTS chat_generated_files`)
	return err
}

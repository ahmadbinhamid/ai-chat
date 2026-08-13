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

// chat_generated_files is a record of every file an assistant message
// changed ("Generated 2 files" in the UI). A new file starts out "pending"
// (staged as a draft, not yet on the real theme) and later becomes
// "applied" or "discarded" — see the 20260813000001 migration, which adds
// the columns that track this. file_path is required, not optional
// metadata: it's the only record of *where* content was written — without
// it this table couldn't answer "what did this message change". chat_id is
// denormalized from message_id so "every file ever touched in this chat"
// doesn't need a join through chat_messages.
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

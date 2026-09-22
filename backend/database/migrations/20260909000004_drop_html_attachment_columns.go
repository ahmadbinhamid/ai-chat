package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20260909000004_drop_html_attachment_columns",
		Up:   Up_20260909000004,
		Down: Down_20260909000004,
	})
}

// images, html_attachment_filename and html_attachment_content are all dead — replaced by
// chat_message_attachments (20260909000002); nothing in Go reads or writes them anymore.
//
// themebuild.GenerateInput's Images/HTMLAttachmentFilename/HTMLAttachmentContent fields are
// unrelated request-path carriers, not columns — unaffected by this migration.
//
// Drops images too, not just the two HTML columns, to avoid a second migration later. Keeps its
// original name — renaming would make the migrator rerun this Up on an already-migrated DB and fail.
//
// Down restores the schema but not the data — nothing retains the original contents, so a
// rollback yields empty columns. No deployment ever had real data here; if that changes, write a backfill first.
func Up_20260909000004(db *sql.DB) error {
	_, err := db.Exec(`
		ALTER TABLE chat_messages
		DROP COLUMN images,
		DROP COLUMN html_attachment_filename,
		DROP COLUMN html_attachment_content
	`)
	return err
}

func Down_20260909000004(db *sql.DB) error {
	_, err := db.Exec(`
		ALTER TABLE chat_messages
		ADD COLUMN images LONGTEXT NULL AFTER created_at,
		ADD COLUMN html_attachment_filename VARCHAR(255) NULL AFTER images,
		ADD COLUMN html_attachment_content LONGTEXT NULL AFTER html_attachment_filename
	`)
	return err
}

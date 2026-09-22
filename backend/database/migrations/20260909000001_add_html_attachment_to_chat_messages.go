package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20260909000001_add_html_attachment_to_chat_messages",
		Up:   Up_20260909000001,
		Down: Down_20260909000001,
	})
}

// html_attachment_filename/content let a user turn carry one reference HTML file — plain
// columns (not a JSON array like images) since this is capped at one file per message.
//
// content is capped at themebuild.MaxHTMLAttachmentBytes and sanitized
// (SanitizeHTMLAttachment strips scripts/base64 assets) before it reaches this column.
func Up_20260909000001(db *sql.DB) error {
	_, err := db.Exec(`
		ALTER TABLE chat_messages
		ADD COLUMN html_attachment_filename VARCHAR(255) NULL AFTER images,
		ADD COLUMN html_attachment_content LONGTEXT NULL AFTER html_attachment_filename
	`)
	return err
}

func Down_20260909000001(db *sql.DB) error {
	_, err := db.Exec(`
		ALTER TABLE chat_messages
		DROP COLUMN html_attachment_filename,
		DROP COLUMN html_attachment_content
	`)
	return err
}

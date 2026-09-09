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

// html_attachment_filename/html_attachment_content let a user-role turn
// carry one reference HTML file (the HTML-attachment feature — attach a
// competitor/reference page's markup, ask the AI to match its structure).
// Two plain columns (not a JSON array like `images`) since this is
// deliberately capped at one file per message, unlike images' 1-5 — see
// chat.Message's own doc comment for why. content is capped at
// themebuild.MaxHTMLAttachmentBytes (enforced in Service.Generate, after
// themebuild.SanitizeHTMLAttachment strips embedded scripts/base64 assets)
// well below LONGTEXT's real ceiling; LONGTEXT is just this codebase's
// existing convention for arbitrary-length text content (see
// chat_generated_files.content).
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

package migrations

import (
	"database/sql"
	"fmt"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20260909000002_create_chat_message_attachments",
		Up:   Up_20260909000002,
		Down: Down_20260909000002,
	})
}

// chat_message_attachments replaces chat_messages' per-type attachment
// columns (images, html_attachment_filename/content — see the 20260908000001
// and 20260909000001 migrations) with one polymorphic table, for two
// reasons: adding a new attachment type no longer costs a migration plus new
// Go fields/scan args/validation constants (it costs one `kind` value and a
// themebuild validation-limit entry), and — the bigger one — a transcript
// read (ListMessagesByChat, which runs on every GET /chat) no longer has to
// pull every attachment's bytes off the row just to report a message
// happened. Bytes still live in MySQL (external object storage was
// considered and rejected — this service runs multi-replica with no shared
// volume, and standing up S3 isn't justified at current volume); what moves
// is WHICH read pays for them: only doGenerate, immediately before it
// builds a model input from a specific message's attachments, not every
// transcript load. See chat.MessageAttachment's own doc comment for the
// read-path split.
//
// content LONGBLOB, not LONGTEXT — deliberate, even for kind='html' (text,
// but opaque here: never SQL-searched, only ever fed to the model or
// decoded back to a string at the one read site that needs it). One column
// type keeps the table genuinely polymorphic; media_type says how to
// interpret the bytes. Raw decoded bytes, not base64: base64 exists to
// carry binary through a text channel like a JSON request body, which is
// exactly where it stays (chat.MessageImage, the wire/write-path DTO) —
// decoded once on the way in, re-encoded transiently only when assembling
// an Anthropic/DeepSeek request body, never stored that way.
//
// storage_key is reserved and unused today (always NULL; content is always
// populated) — it exists so moving bytes to external storage later is a
// config change plus a copy job, not a schema redesign. Exactly one of
// content/storage_key is meaningful for a given row.
//
// checksum is sha256 of the raw bytes, tenant-scoped indexed (integrity now;
// dedupe later — recorded, not acted on: two rows can share a checksum and
// each still owns its own bytes, so deleting one can never strip another's
// content out from under it).
//
// No updated_at: unlike chat_generated_files (whose rows transition
// pending -> applied/discarded after insert, see the 20260813000001
// migration), a row here is never mutated after creation — only inserted,
// and removed automatically via ON DELETE CASCADE when its message's chat
// is deleted. That cascade is a real advantage over external storage: no
// orphan-blob sweeper needed, the database reclaims the bytes itself.
//
// Old columns are deliberately NOT dropped here — see Down and the
// migration's own backfill below. A migration that both moves data and
// destroys the source in one step has no recovery path; dropping
// images/html_attachment_* is a separate, later migration once the backfill
// is verified in production.
func Up_20260909000002(db *sql.DB) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS chat_message_attachments (
		  id           CHAR(36)        NOT NULL PRIMARY KEY,
		  message_id   CHAR(36)        NOT NULL,
		  tenant_id    BIGINT UNSIGNED NOT NULL,
		  kind         VARCHAR(32)     NOT NULL,
		  filename     VARCHAR(255)    NOT NULL,
		  media_type   VARCHAR(127)    NOT NULL,
		  size_bytes   BIGINT UNSIGNED NOT NULL,
		  checksum     CHAR(64)        NOT NULL,
		  position     INT UNSIGNED    NOT NULL DEFAULT 0,
		  content      LONGBLOB        NULL,
		  storage_key  VARCHAR(512)    NULL,
		  created_at   DATETIME        NOT NULL,
		  CONSTRAINT fk_cma_message FOREIGN KEY (message_id) REFERENCES chat_messages (id) ON DELETE CASCADE,
		  INDEX idx_cma_message_position (message_id, position),
		  INDEX idx_cma_tenant_checksum (tenant_id, checksum)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
	`); err != nil {
		return fmt.Errorf("create chat_message_attachments: %w", err)
	}

	// Backfill images: one row per images[] array entry, decoded straight
	// from base64 to bytes via JSON_TABLE + FROM_BASE64 — no application
	// code, no auth, pure SQL (confirmed available on this deployment's
	// MySQL: JSON_TABLE, FROM_BASE64, SHA2, UUID() are all stdlib SQL
	// functions here, not an extension). ord is JSON_TABLE's own 1-based
	// array index — position stores it 0-based (ord - 1) to match
	// chat.MessageAttachment.Position's own indexing, and the filename's
	// display number (image-N) uses ord directly, which is exactly
	// position+1 — the same rule chat.filenameForImage uses for images
	// attached going forward, so a backfilled row and a freshly-written row
	// name themselves identically. The old images column carried no
	// filename at all (see the 20260908000001 migration), so this is a
	// synthesized-but-stable name, not a recovered one.
	if _, err := db.Exec(`
		INSERT INTO chat_message_attachments
		  (id, message_id, tenant_id, kind, filename, media_type, size_bytes, checksum, position, content, storage_key, created_at)
		SELECT
		  UUID(),
		  cm.id,
		  cm.tenant_id,
		  'image',
		  CONCAT('image-', jt.ord, '.',
		    CASE jt.media_type
		      WHEN 'image/png'  THEN 'png'
		      WHEN 'image/jpeg' THEN 'jpg'
		      WHEN 'image/gif'  THEN 'gif'
		      WHEN 'image/webp' THEN 'webp'
		      ELSE 'bin'
		    END
		  ),
		  jt.media_type,
		  LENGTH(FROM_BASE64(jt.base64)),
		  SHA2(FROM_BASE64(jt.base64), 256),
		  jt.ord - 1,
		  FROM_BASE64(jt.base64),
		  NULL,
		  cm.created_at
		FROM chat_messages cm
		JOIN JSON_TABLE(
		  cm.images,
		  '$[*]' COLUMNS (
		    ord        FOR ORDINALITY,
		    base64     LONGTEXT     PATH '$.base64',
		    media_type VARCHAR(127) PATH '$.media_type'
		  )
		) AS jt
		WHERE cm.images IS NOT NULL;
	`); err != nil {
		return fmt.Errorf("backfill image attachments: %w", err)
	}

	// Backfill the single HTML attachment pair, kind='html', position 0
	// (there was and is never more than one per message — see
	// chat.MessageAttachment's own doc comment on why HTML stays capped
	// tighter than images). filename carries straight over: unlike images,
	// the old columns already had a real client-supplied filename.
	// CAST(... AS BINARY) takes the utf8mb4 TEXT column's own raw bytes
	// into the BLOB column — the same bytes LENGTH()/SHA2() below measure
	// and hash, so size_bytes/checksum describe exactly what's stored.
	if _, err := db.Exec(`
		INSERT INTO chat_message_attachments
		  (id, message_id, tenant_id, kind, filename, media_type, size_bytes, checksum, position, content, storage_key, created_at)
		SELECT
		  UUID(),
		  cm.id,
		  cm.tenant_id,
		  'html',
		  cm.html_attachment_filename,
		  'text/html',
		  LENGTH(cm.html_attachment_content),
		  SHA2(CAST(cm.html_attachment_content AS BINARY), 256),
		  0,
		  CAST(cm.html_attachment_content AS BINARY),
		  NULL,
		  cm.created_at
		FROM chat_messages cm
		WHERE cm.html_attachment_content IS NOT NULL
		  AND cm.html_attachment_filename IS NOT NULL;
	`); err != nil {
		return fmt.Errorf("backfill html attachments: %w", err)
	}

	return nil
}

func Down_20260909000002(db *sql.DB) error {
	_, err := db.Exec(`DROP TABLE IF EXISTS chat_message_attachments`)
	return err
}

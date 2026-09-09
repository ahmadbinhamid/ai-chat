package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
)

// openTestDB connects to the same MySQL the backend's own .env points at —
// matches chat/themebuild's own openTestDB helpers. Skips (not fails) when
// unreachable, same reasoning as those.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&charset=utf8mb4&loc=UTC",
		getenv("DB_USERNAME", "root"), os.Getenv("DB_PASSWORD"),
		getenv("DB_HOST", "127.0.0.1"), getenv("DB_PORT", "3306"), getenv("DB_DATABASE", "ai_chat"))

	conn, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Skipf("skipping: could not open test database: %v", err)
	}
	if err := conn.Ping(); err != nil {
		_ = conn.Close()
		t.Skipf("skipping: test database not reachable: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// TestBackfill_ImagesAndHTML_ProduceCorrectAttachmentRows seeds one
// chat_messages row, in the OLD columns (images JSON array + html_attachment_*),
// exactly the shape existing production rows have — then runs the same
// backfill logic Up_20260909000002 runs (see the queries below, which
// intentionally mirror the migration's own SQL — a migration file, once
// shipped, is never edited again in this codebase's own convention, so
// there's no meaningful drift risk in duplicating its query shape here)
// scoped to just this one seeded message (AND cm.id = ?), so it's safe to
// run against a database the real migration has already run against
// unscoped, without creating duplicate rows for unrelated existing data.
//
// Requires chat_message_attachments to already exist — i.e. `make migrate`
// has been run against this database (see the migration's own Verification
// note) — same precondition every other DB-backed test in this repo has for
// its own tables.
//
// Also requires images/html_attachment_* to still exist on chat_messages —
// no longer true on any database that has run the 20260909000004 migration
// (which drops all three; see its own doc comment). That's expected, not a
// gap: those columns are gone for good by design, so seeding a fixture into
// them can never be exercised again once a database has crossed that point
// — skip rather than fail, the same way openTestDB skips when the database
// itself isn't reachable at all.
func TestBackfill_ImagesAndHTML_ProduceCorrectAttachmentRows(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	var legacyColumnCount int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = 'chat_messages' AND column_name = 'html_attachment_filename'
	`).Scan(&legacyColumnCount); err != nil {
		t.Fatalf("could not check for html_attachment_filename: %v", err)
	}
	if legacyColumnCount == 0 {
		t.Skip("skipping: html_attachment_filename no longer exists on chat_messages (20260909000004 has already run on this database) — this test's fixture setup can't seed it; the backfill it validates already ran, once, in production history")
	}

	chatID := uuid.NewString()
	messageID := uuid.NewString()
	tenantID := uint64(time.Now().UnixNano())
	now := time.Now().UTC().Truncate(time.Second)

	if _, err := db.ExecContext(ctx, `
		INSERT INTO chats (id, tenant_id, type, total_input_tokens, total_output_tokens, created_at, updated_at)
		VALUES (?, ?, ?, 0, 0, ?, ?)
	`, chatID, tenantID, "backfill-test-"+chatID, now, now); err != nil {
		t.Fatalf("seed chat failed: %v", err)
	}

	// Three images (base64 of "img-0"/"img-1"/"img-2") + one HTML
	// attachment — the exact shape TestBackfill's own doc comment
	// describes: 3 images plus one HTML file, expecting 4 resulting rows.
	imagesJSON := `[
		{"base64":"aW1nLTA=","media_type":"image/png"},
		{"base64":"aW1nLTE=","media_type":"image/jpeg"},
		{"base64":"aW1nLTI=","media_type":"image/webp"}
	]`
	htmlFilename := "reference.html"
	htmlContent := "<h1>Reference</h1>"
	userID := uint64(1)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO chat_messages
		  (id, chat_id, tenant_id, role, user_id, content, status, apply_status, created_at, images, html_attachment_filename, html_attachment_content)
		VALUES (?, ?, ?, 'user', ?, 'redesign this', 'completed', 'not_applicable', ?, ?, ?, ?)
	`, messageID, chatID, tenantID, userID, now, imagesJSON, htmlFilename, htmlContent); err != nil {
		t.Fatalf("seed chat_messages failed: %v", err)
	}

	// Mirrors Up_20260909000002's own image-backfill INSERT...SELECT,
	// scoped to this one message.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO chat_message_attachments
		  (id, message_id, tenant_id, kind, filename, media_type, size_bytes, checksum, position, content, storage_key, created_at)
		SELECT
		  UUID(), cm.id, cm.tenant_id, 'image',
		  CONCAT('image-', jt.ord, '.',
		    CASE jt.media_type
		      WHEN 'image/png'  THEN 'png'
		      WHEN 'image/jpeg' THEN 'jpg'
		      WHEN 'image/gif'  THEN 'gif'
		      WHEN 'image/webp' THEN 'webp'
		      ELSE 'bin'
		    END
		  ),
		  jt.media_type, LENGTH(FROM_BASE64(jt.base64)), SHA2(FROM_BASE64(jt.base64), 256), jt.ord - 1,
		  FROM_BASE64(jt.base64), NULL, cm.created_at
		FROM chat_messages cm
		JOIN JSON_TABLE(
		  cm.images, '$[*]' COLUMNS (
		    ord FOR ORDINALITY,
		    base64 LONGTEXT PATH '$.base64',
		    media_type VARCHAR(127) PATH '$.media_type'
		  )
		) AS jt
		WHERE cm.images IS NOT NULL AND cm.id = ?;
	`, messageID); err != nil {
		t.Fatalf("backfill images failed: %v", err)
	}

	// Mirrors Up_20260909000002's own HTML-backfill INSERT...SELECT,
	// scoped to this one message.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO chat_message_attachments
		  (id, message_id, tenant_id, kind, filename, media_type, size_bytes, checksum, position, content, storage_key, created_at)
		SELECT
		  UUID(), cm.id, cm.tenant_id, 'html', cm.html_attachment_filename, 'text/html',
		  LENGTH(cm.html_attachment_content), SHA2(CAST(cm.html_attachment_content AS BINARY), 256), 0,
		  CAST(cm.html_attachment_content AS BINARY), NULL, cm.created_at
		FROM chat_messages cm
		WHERE cm.html_attachment_content IS NOT NULL AND cm.html_attachment_filename IS NOT NULL AND cm.id = ?;
	`, messageID); err != nil {
		t.Fatalf("backfill html failed: %v", err)
	}

	rows, err := db.QueryContext(ctx, `
		SELECT kind, filename, media_type, size_bytes, checksum, position, content
		FROM chat_message_attachments WHERE message_id = ? ORDER BY kind, position
	`, messageID)
	if err != nil {
		t.Fatalf("query attachments failed: %v", err)
	}
	defer rows.Close()

	type row struct {
		kind, filename, mediaType, checksum string
		sizeBytes                           int64
		position                            int
		content                             []byte
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.kind, &r.filename, &r.mediaType, &r.sizeBytes, &r.checksum, &r.position, &r.content); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}

	if len(got) != 4 {
		t.Fatalf("expected 4 attachment rows (3 images + 1 html), got %d: %+v", len(got), got)
	}

	// html sorts before image alphabetically — 'h' < 'i'.
	wantHTML := row{kind: "html", filename: "reference.html", mediaType: "text/html", position: 0, content: []byte("<h1>Reference</h1>")}
	checkSum := func(content []byte) string {
		sum := sha256.Sum256(content)
		return hex.EncodeToString(sum[:])
	}
	if got[0].kind != wantHTML.kind || got[0].filename != wantHTML.filename || got[0].mediaType != wantHTML.mediaType ||
		got[0].position != wantHTML.position || string(got[0].content) != string(wantHTML.content) {
		t.Errorf("html row mismatch: got %+v", got[0])
	}
	if got[0].sizeBytes != int64(len(wantHTML.content)) {
		t.Errorf("html size_bytes: want %d, got %d", len(wantHTML.content), got[0].sizeBytes)
	}
	if got[0].checksum != checkSum(wantHTML.content) {
		t.Errorf("html checksum: want %s, got %s", checkSum(wantHTML.content), got[0].checksum)
	}

	wantImages := []row{
		{kind: "image", filename: "image-1.png", mediaType: "image/png", position: 0, content: []byte("img-0")},
		{kind: "image", filename: "image-2.jpg", mediaType: "image/jpeg", position: 1, content: []byte("img-1")},
		{kind: "image", filename: "image-3.webp", mediaType: "image/webp", position: 2, content: []byte("img-2")},
	}
	for i, want := range wantImages {
		g := got[i+1]
		if g.filename != want.filename || g.mediaType != want.mediaType || g.position != want.position || string(g.content) != string(want.content) {
			t.Errorf("image row %d mismatch: want %+v, got %+v", i, want, g)
		}
		if g.sizeBytes != int64(len(want.content)) {
			t.Errorf("image row %d size_bytes: want %d, got %d", i, len(want.content), g.sizeBytes)
		}
		if g.checksum != checkSum(want.content) {
			t.Errorf("image row %d checksum: want %s, got %s", i, checkSum(want.content), g.checksum)
		}
	}
}

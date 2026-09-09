package chat

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
)

// openTestDB connects to the same MySQL this repo's .env already points at
// and skips the test if it isn't reachable — matches themebuild's own
// openTestDB (see internal/modules/themebuild/generation_test.go). This
// package's own DB tests need a real database for the same class of reason
// themebuild's do: clientFoundRows=true (set in the DSN below) is a
// MySQL-driver-level connection option, not something a fake/mock
// database/sql driver can be trusted to reproduce faithfully — see
// TestRepository_TouchChatUsage_ZeroDeltaSameSecond, the test this
// specifically exists for.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&charset=utf8mb4&loc=UTC&clientFoundRows=true",
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

func seedChat(t *testing.T, repo *Repository, chatType string) Chat {
	t.Helper()
	now := time.Now().UTC()
	c := Chat{
		ID:        uuid.NewString(),
		TenantID:  1,
		Type:      chatType,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := repo.CreateChat(context.Background(), c); err != nil {
		t.Fatalf("seedChat: CreateChat failed: %v", err)
	}
	return c
}

func TestRepository_CreateMessageAndTouchUsage_CommitsBothWrites(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()

	c := seedChat(t, repo, "builder-"+uuid.NewString())

	now := time.Now().UTC()
	m := Message{
		ID:          uuid.NewString(),
		ChatID:      c.ID,
		TenantID:    c.TenantID,
		Role:        RoleAssistant,
		Content:     "done",
		Status:      MessageStatusCompleted,
		ApplyStatus: ApplyStatusNotApplicable,
		CreatedAt:   now,
	}
	if err := repo.CreateMessageAndTouchUsage(ctx, m, nil, 10, 20, now); err != nil {
		t.Fatalf("CreateMessageAndTouchUsage failed: %v", err)
	}

	gotMsg, err := repo.GetMessageByID(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMessageByID failed: %v", err)
	}
	if gotMsg.Content != "done" {
		t.Fatalf("expected the message row to be committed, got %+v", gotMsg)
	}

	gotChat, err := repo.GetChatByID(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetChatByID failed: %v", err)
	}
	if gotChat.TotalInputTokens != 10 || gotChat.TotalOutputTokens != 20 {
		t.Fatalf("expected the chat's usage totals to be committed alongside the message, got %+v", gotChat)
	}
}

// TestRepository_CreateMessageAndTouchUsage_RollsBackOnFailure forces
// createMessage's own INSERT to fail (chk_chat_messages_user_role rejects a
// 'user'-role row with no user_id) and checks nothing lands durably as a
// result. This is the only failure mode reachable through this repository's
// public API: fk_chat_messages_chat means touchChatUsage can never run
// against a chat_id that createMessage's own INSERT didn't already require
// to exist, so a genuine "step one committed, step two failed" scenario
// isn't constructible from outside the transaction — this test instead
// verifies the transaction-wrapping actually works, i.e. a failure here
// leaves the target chat's usage totals untouched, not incremented by a
// message that was never really recorded.
func TestRepository_CreateMessageAndTouchUsage_RollsBackOnFailure(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()

	c := seedChat(t, repo, "builder-"+uuid.NewString())

	now := time.Now().UTC()
	invalid := Message{
		ID:        uuid.NewString(),
		ChatID:    c.ID,
		TenantID:  c.TenantID,
		Role:      RoleUser, // user_id left nil — violates chk_chat_messages_user_role
		Content:   "should never land",
		Status:    MessageStatusCompleted,
		CreatedAt: now,
	}
	if err := repo.CreateMessageAndTouchUsage(ctx, invalid, nil, 99, 99, now); err == nil {
		t.Fatal("expected CreateMessageAndTouchUsage to fail for a user-role message with no user_id")
	}

	if _, err := repo.GetMessageByID(ctx, invalid.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected the rejected message to never be persisted, got err=%v", err)
	}

	gotChat, err := repo.GetChatByID(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetChatByID failed: %v", err)
	}
	if gotChat.TotalInputTokens != 0 || gotChat.TotalOutputTokens != 0 {
		t.Fatalf("expected the chat's usage totals to be rolled back untouched, got %+v", gotChat)
	}
}

// TestRepository_TouchChatUsage_ZeroDeltaSameSecond is the critical test
// item 5 calls out by name: touchChatUsage's UPDATE sets total_input_tokens
// = total_input_tokens + 0 and updated_at to a value that can land in the
// exact same wall-clock second as the row's current updated_at (created_at
// == updated_at at seed time, both second-precision DATETIME columns).
// Without clientFoundRows=true in the connection DSN, MySQL's default
// affected-rows semantics report 0 rows affected for an UPDATE that
// changed nothing byte-for-byte — which checkAffected would then
// misreport as ErrNotFound for a chat that very much still exists. This
// only ever surfaces with a real MySQL connection (see openTestDB's doc
// comment), which is why this test — unlike touchChatUsage's caller-level
// behavior — can't be verified with a fake/mock driver.
func TestRepository_TouchChatUsage_ZeroDeltaSameSecond(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()

	c := seedChat(t, repo, "builder-"+uuid.NewString())

	// Same instant as the seed's created_at/updated_at — the whole point is
	// to land in the same wall-clock second, not merely a nearby one.
	err := touchChatUsage(ctx, repo.db, c.ID, 0, 0, c.CreatedAt)
	if err != nil {
		t.Fatalf("touchChatUsage with a zero delta unexpectedly failed (likely a clientFoundRows regression): %v", err)
	}
}

func TestRepository_GetChatByTenantAndType_ReturnsErrNotFoundForTenantWithNoChat(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()

	_, err := repo.GetChatByTenantAndType(ctx, 999999999, "builder-"+uuid.NewString())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for a tenant with no chat of this type, got %v", err)
	}
}

// TestService_RecordUserMessage_ListReturnsAttachmentMetadataNoContent
// round-trips a message with two images and one HTML attachment through
// the real write path (chat.Service.RecordUserMessage, which decodes wire
// base64 into raw bytes — see buildAttachments) and the real transcript
// read path (ListMessagesByChat), asserting metadata comes back complete,
// correctly ordered/positioned, and — the actual point of the
// metadata/content split — that Content is nil on every attachment: a
// transcript read must never carry attachment bytes.
func TestService_RecordUserMessage_ListReturnsAttachmentMetadataNoContent(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	svc := NewService(repo)
	ctx := context.Background()

	c := seedChat(t, repo, "builder-"+uuid.NewString())
	userID := uint64(42)
	filename := "reference.html"
	htmlContent := "<h1>Reference</h1>"
	images := []MessageImage{
		{Base64: "aW1hZ2Utb25l", MediaType: "image/png"},  // "image-one"
		{Base64: "aW1hZ2UtdHdv", MediaType: "image/jpeg"}, // "image-two"
	}

	created, err := svc.RecordUserMessage(ctx, c, &userID, "", "", "redesign this", images, &filename, &htmlContent)
	if err != nil {
		t.Fatalf("RecordUserMessage failed: %v", err)
	}

	messages, err := repo.ListMessagesByChat(ctx, c.ID)
	if err != nil {
		t.Fatalf("ListMessagesByChat failed: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	got := messages[0]

	if len(got.Attachments) != 3 {
		t.Fatalf("expected 3 attachments (2 images + 1 html), got %d: %+v", len(got.Attachments), got.Attachments)
	}
	// Ordered by (kind, position) — see listAttachmentMetadata's own doc
	// comment on why kind is the tiebreaker: html and the first image both
	// have position 0 (position is scoped per kind, not per message), and
	// "html" sorts before "image" alphabetically.
	html, img0, img1 := got.Attachments[0], got.Attachments[1], got.Attachments[2]

	if img0.Kind != AttachmentKindImage || img0.Filename != "image-1.png" || img0.MediaType != "image/png" || img0.Position != 0 {
		t.Errorf("unexpected first image metadata: %+v", img0)
	}
	if img0.SizeBytes != int64(len("image-one")) {
		t.Errorf("expected first image size_bytes %d, got %d", len("image-one"), img0.SizeBytes)
	}
	if img1.Kind != AttachmentKindImage || img1.Filename != "image-2.jpg" || img1.MediaType != "image/jpeg" || img1.Position != 1 {
		t.Errorf("unexpected second image metadata: %+v", img1)
	}
	if html.Kind != AttachmentKindHTML || html.Filename != filename || html.MediaType != "text/html" || html.Position != 0 {
		t.Errorf("unexpected html metadata: %+v", html)
	}
	if html.SizeBytes != int64(len(htmlContent)) {
		t.Errorf("expected html size_bytes %d, got %d", len(htmlContent), html.SizeBytes)
	}

	for _, a := range got.Attachments {
		if a.Content != nil {
			t.Errorf("expected ListMessagesByChat to never populate Content, got attachment %+v with Content set", a)
		}
	}

	// Sanity: RecordUserMessage's own return value also carries no
	// unexpected extra rows and matches what got persisted.
	if len(created.Attachments) != 3 {
		t.Fatalf("expected RecordUserMessage's own return value to report 3 attachments, got %d", len(created.Attachments))
	}
}

// TestRepository_ListMessagesByChat_NoImagesFieldOnlyAttachments supersedes
// what used to be TestRepository_ListMessagesByChat_ReturnsBothImagesShimAndAttachments
// from the previous pass, which asserted the OPPOSITE of what this asserts:
// that GET /chat carried both a deprecated images[] shim AND attachments[]
// at once. That shim (and the chat.Message.Images field backing it) is now
// gone entirely — this feature never shipped past one local branch owned by
// one person, so there was no deployed frontend to decouple a deploy for —
// so the old test's very subject no longer exists; it couldn't be
// "call-site updated" without inverting its own assertion, which is what
// this replacement does. GET /chat (backed by ListMessagesByChat, embedded
// via messageWithFiles) must return attachments[] metadata and must NOT
// carry an images key at all.
func TestRepository_ListMessagesByChat_NoImagesFieldOnlyAttachments(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	svc := NewService(repo)
	ctx := context.Background()

	c := seedChat(t, repo, "builder-"+uuid.NewString())
	userID := uint64(42)
	images := []MessageImage{{Base64: "aGVsbG8=", MediaType: "image/png"}} // "hello"

	if _, err := svc.RecordUserMessage(ctx, c, &userID, "", "", "look at this", images, nil, nil); err != nil {
		t.Fatalf("RecordUserMessage failed: %v", err)
	}

	messages, err := repo.ListMessagesByChat(ctx, c.ID)
	if err != nil {
		t.Fatalf("ListMessagesByChat failed: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	got := messages[0]

	if len(got.Attachments) != 1 || got.Attachments[0].Kind != AttachmentKindImage || got.Attachments[0].Content != nil {
		t.Fatalf("expected Attachments metadata (no content), got %+v", got.Attachments)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	out := string(raw)
	if strings.Contains(out, `"images"`) {
		t.Errorf("expected the deprecated images key to be gone entirely, got: %s", out)
	}
	if !strings.Contains(out, `"attachments"`) {
		t.Errorf("expected the attachments key to be present in JSON, got: %s", out)
	}
}

// TestRepository_GetAttachmentsContent_ReturnsDecodedBytes proves the one
// content-fetching read path returns the RAW DECODED bytes — not base64,
// no padding characters — with size_bytes and checksum matching those
// decoded bytes exactly. This is the byte-identical round-trip the
// LONGBLOB/no-base64-at-rest design depends on.
func TestRepository_GetAttachmentsContent_ReturnsDecodedBytes(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	svc := NewService(repo)
	ctx := context.Background()

	c := seedChat(t, repo, "builder-"+uuid.NewString())
	userID := uint64(42)
	// "hello world" base64-encoded, WITH padding, so a passing test proves
	// the stored bytes are the decoded plaintext, not a copy of the base64
	// string (which would still contain the '=' padding character).
	images := []MessageImage{{Base64: "aGVsbG8gd29ybGQ=", MediaType: "image/png"}}

	created, err := svc.RecordUserMessage(ctx, c, &userID, "", "", "look at this", images, nil, nil)
	if err != nil {
		t.Fatalf("RecordUserMessage failed: %v", err)
	}

	full, err := repo.GetAttachmentsContent(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetAttachmentsContent failed: %v", err)
	}
	if len(full) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(full))
	}
	got := full[0]

	if string(got.Content) != "hello world" {
		t.Fatalf("expected decoded content %q, got %q", "hello world", string(got.Content))
	}
	if strings.ContainsRune(string(got.Content), '=') {
		t.Fatalf("content still looks like base64 (contains padding), got %q", string(got.Content))
	}
	if got.SizeBytes != int64(len("hello world")) {
		t.Errorf("expected size_bytes %d, got %d", len("hello world"), got.SizeBytes)
	}
	wantSum := sha256.Sum256([]byte("hello world"))
	if got.Checksum != hex.EncodeToString(wantSum[:]) {
		t.Errorf("expected checksum %s, got %s", hex.EncodeToString(wantSum[:]), got.Checksum)
	}
}

// TestRepository_ListMessagesByChat_SkipsUnknownAttachmentKind inserts a
// chat_message_attachments row directly with a kind this build has never
// heard of (simulating a future version's data, or corruption) and proves
// ListMessagesByChat skips it silently rather than erroring the whole
// transcript read.
func TestRepository_ListMessagesByChat_SkipsUnknownAttachmentKind(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()

	c := seedChat(t, repo, "builder-"+uuid.NewString())
	userID := uint64(42)
	now := time.Now().UTC()
	m := Message{
		ID:          uuid.NewString(),
		ChatID:      c.ID,
		TenantID:    c.TenantID,
		Role:        RoleUser,
		UserID:      &userID,
		Content:     "attach a pdf",
		Status:      MessageStatusCompleted,
		ApplyStatus: ApplyStatusNotApplicable,
		CreatedAt:   now,
	}
	if err := repo.CreateMessageAndTouchUsage(ctx, m, nil, 0, 0, now); err != nil {
		t.Fatalf("CreateMessageAndTouchUsage failed: %v", err)
	}
	dummyChecksum := strings.Repeat("0", 64)
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO chat_message_attachments (id, message_id, tenant_id, kind, filename, media_type, size_bytes, checksum, position, content, storage_key, created_at, updated_at)
		VALUES (?, ?, ?, 'pdf', 'spec.pdf', 'application/pdf', 3, ?, 0, ?, NULL, ?, ?)
	`, uuid.NewString(), m.ID, c.TenantID, dummyChecksum, []byte("pdf"), now, now); err != nil {
		t.Fatalf("insert unknown-kind attachment failed: %v", err)
	}

	messages, err := repo.ListMessagesByChat(ctx, c.ID)
	if err != nil {
		t.Fatalf("expected ListMessagesByChat to succeed despite the unknown-kind row, got: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if len(messages[0].Attachments) != 0 {
		t.Errorf("expected the unknown-kind attachment to be skipped, got %+v", messages[0].Attachments)
	}
}

// TestRepository_AttachmentUniqueKey_RejectsDuplicateMessageKindPosition
// proves uq_cma_message_kind_position (20260909000003 migration) actually
// enforces at the write layer what the ORDER BY message_id, kind, position
// tiebreak only made deterministic at read time: two rows sharing
// (message_id, kind, position) must be impossible, not merely unlikely.
// Requires `make migrate` to have run that migration against this database.
func TestRepository_AttachmentUniqueKey_RejectsDuplicateMessageKindPosition(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()

	c := seedChat(t, repo, "builder-"+uuid.NewString())
	userID := uint64(42)
	now := time.Now().UTC()
	m := Message{
		ID:          uuid.NewString(),
		ChatID:      c.ID,
		TenantID:    c.TenantID,
		Role:        RoleUser,
		UserID:      &userID,
		Content:     "attach an image",
		Status:      MessageStatusCompleted,
		ApplyStatus: ApplyStatusNotApplicable,
		CreatedAt:   now,
	}
	if err := repo.CreateMessageAndTouchUsage(ctx, m, nil, 0, 0, now); err != nil {
		t.Fatalf("CreateMessageAndTouchUsage failed: %v", err)
	}

	insertAttachment := func() error {
		_, err := conn.ExecContext(ctx, `
			INSERT INTO chat_message_attachments (id, message_id, tenant_id, kind, filename, media_type, size_bytes, checksum, position, content, storage_key, created_at, updated_at)
			VALUES (?, ?, ?, 'image', 'image-1.png', 'image/png', 4, ?, 0, ?, NULL, ?, ?)
		`, uuid.NewString(), m.ID, c.TenantID, strings.Repeat("0", 64), []byte("AAAA"), now, now)
		return err
	}
	if err := insertAttachment(); err != nil {
		t.Fatalf("first insert at (message_id, kind=image, position=0) unexpectedly failed: %v", err)
	}
	if err := insertAttachment(); err == nil {
		t.Fatal("expected a second row at the same (message_id, kind, position) to be rejected by uq_cma_message_kind_position, got no error")
	}
}

// TestRepository_GetMessageByID_OmitsAttachments proves GetMessageByID's
// narrower query (no attachment join at all — see its own doc comment on
// why: its only caller, revert, never reads them) leaves Attachments nil,
// even for a message that genuinely has some — distinguishing "correctly
// not loaded" from "happened to have none".
func TestRepository_GetMessageByID_OmitsAttachments(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	svc := NewService(repo)
	ctx := context.Background()

	c := seedChat(t, repo, "builder-"+uuid.NewString())
	userID := uint64(42)
	filename := "reference.html"
	htmlContent := "<h1>Reference</h1>"
	images := []MessageImage{{Base64: "AAAA", MediaType: "image/png"}}

	created, err := svc.RecordUserMessage(ctx, c, &userID, "", "", "redesign this", images, &filename, &htmlContent)
	if err != nil {
		t.Fatalf("RecordUserMessage failed: %v", err)
	}

	got, err := repo.GetMessageByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetMessageByID failed: %v", err)
	}
	if got.Content != "redesign this" {
		t.Fatalf("expected the non-attachment fields to still scan correctly, got %+v", got)
	}
	if got.Attachments != nil {
		t.Errorf("expected GetMessageByID to omit Attachments, got %+v", got.Attachments)
	}
}

func TestService_GetChat_OtherTenantsChatReturnsErrNotFound(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	svc := NewService(repo)
	ctx := context.Background()

	owner := uint64(1) // matches seedChat's hardcoded TenantID
	c := seedChat(t, repo, "builder-"+uuid.NewString())

	_, err := svc.GetChat(ctx, owner+1, c.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound (not a distinct permission error) for another tenant's chat, got %v", err)
	}

	// Sanity check: the owning tenant can still fetch it — proves the
	// above failed on ownership, not on a broken lookup.
	if _, err := svc.GetChat(ctx, owner, c.ID); err != nil {
		t.Fatalf("expected the owning tenant to fetch its own chat, got %v", err)
	}
}

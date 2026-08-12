package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
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
	if err := repo.CreateMessageAndTouchUsage(ctx, m, 10, 20, now); err != nil {
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
	if err := repo.CreateMessageAndTouchUsage(ctx, invalid, 99, 99, now); err == nil {
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

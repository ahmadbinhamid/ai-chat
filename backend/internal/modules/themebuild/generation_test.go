package themebuild

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

// openTestDB connects to the same MySQL this repo's .env already points
// at (DB_HOST/DB_PORT/DB_DATABASE/DB_USERNAME/DB_PASSWORD, defaulting to
// the .env.example values) and skips the test if it isn't reachable —
// these are the only tests in this package that need a real database,
// since the generations table's uniq_generations_running_chat virtual-
// column index is exactly the kind of thing worth verifying against real
// MySQL rather than assuming from reading the SQL.
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

func TestGenerationRepository_StartEndGetLifecycle(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()

	if _, err := repo.GetGeneration(ctx, chatID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for a chat with no generation yet, got %v", err)
	}

	genID := uuid.NewString()
	if err := repo.StartGeneration(ctx, genID, chatID, 1); err != nil {
		t.Fatalf("StartGeneration failed: %v", err)
	}

	g, err := repo.GetGeneration(ctx, chatID)
	if err != nil {
		t.Fatalf("GetGeneration failed: %v", err)
	}
	if g.Status != GenerationStatusRunning || g.ChatID != chatID || g.Attempts != 0 {
		t.Fatalf("unexpected generation state after start: %+v", g)
	}

	if err := repo.SetGenerationAttempts(ctx, chatID, 2); err != nil {
		t.Fatalf("SetGenerationAttempts failed: %v", err)
	}
	g, err = repo.GetGeneration(ctx, chatID)
	if err != nil {
		t.Fatalf("GetGeneration failed: %v", err)
	}
	if g.Attempts != 2 {
		t.Fatalf("expected attempts=2, got %d", g.Attempts)
	}

	if err := repo.EndGeneration(ctx, chatID, fmt.Errorf("boom")); err != nil {
		t.Fatalf("EndGeneration failed: %v", err)
	}
	g, err = repo.GetGeneration(ctx, chatID)
	if err != nil {
		t.Fatalf("GetGeneration failed: %v", err)
	}
	// EndGeneration sanitizes genErr before storing it (see ai.SanitizeError)
	// — the raw "boom" never reaches this column, only its generic fallback
	// wrapping, since "boom" matches no recognized category.
	wantErr := "Error from AI agent: something went wrong while generating a response — please try again in a moment"
	if g.Status != GenerationStatusFailed || g.Error == nil || *g.Error != wantErr || g.FinishedAt == nil {
		t.Fatalf("unexpected generation state after end: %+v", g)
	}
}

func TestGenerationRepository_StartGenerationRejectsSecondConcurrentRun(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()

	if err := repo.StartGeneration(ctx, uuid.NewString(), chatID, 1); err != nil {
		t.Fatalf("first StartGeneration failed: %v", err)
	}
	// The uniq_generations_running_chat virtual-column index — not
	// application-level locking — is what must reject this.
	err := repo.StartGeneration(ctx, uuid.NewString(), chatID, 1)
	if !errors.Is(err, ErrGenerationInProgress) {
		t.Fatalf("expected ErrGenerationInProgress for a second concurrent start, got %v", err)
	}

	// Once the first finishes, a new one is allowed again (running_chat_id
	// goes back to NULL, which the unique index never constrains).
	if err := repo.EndGeneration(ctx, chatID, nil); err != nil {
		t.Fatalf("EndGeneration failed: %v", err)
	}
	if err := repo.StartGeneration(ctx, uuid.NewString(), chatID, 1); err != nil {
		t.Fatalf("expected a new start to succeed after the previous one ended, got %v", err)
	}
}

func TestGenerationRepository_ReapStaleGenerations(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()
	genID := uuid.NewString()

	if _, err := conn.ExecContext(ctx, `
		INSERT INTO generations (id, chat_id, tenant_id, status, attempts, prompt, started_at)
		VALUES (?, ?, 1, ?, 0, ?, ?)
	`, genID, chatID, GenerationStatusRunning, "", time.Now().UTC().Add(-10*time.Minute)); err != nil {
		t.Fatalf("failed to seed a stale running generation: %v", err)
	}

	n, err := repo.ReapStaleGenerations(ctx, 5*time.Minute, 5*time.Minute)
	if err != nil {
		t.Fatalf("ReapStaleGenerations failed: %v", err)
	}
	if n < 1 {
		t.Fatalf("expected at least 1 row reaped, got %d", n)
	}

	g, err := repo.GetGeneration(ctx, chatID)
	if err != nil {
		t.Fatalf("GetGeneration failed: %v", err)
	}
	if g.Status != GenerationStatusFailed || g.Error == nil {
		t.Fatalf("expected the stale generation to be marked failed, got %+v", g)
	}
}

// TestGenerationRepository_ReapStaleGenerations_HeartbeatOverridesStartedAt
// covers the case the 20260813000002 migration exists for: a generation
// whose started_at is old (well past what would be a stale threshold on
// its own) but whose last_heartbeat_at is recent must NOT be reaped — a
// live generation still emitting progress events must survive exactly
// because those events are stamping the heartbeat, regardless of how long
// ago it started.
func TestGenerationRepository_ReapStaleGenerations_HeartbeatOverridesStartedAt(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()
	genID := uuid.NewString()

	if _, err := conn.ExecContext(ctx, `
		INSERT INTO generations (id, chat_id, tenant_id, status, attempts, prompt, started_at, last_heartbeat_at)
		VALUES (?, ?, 1, ?, 0, ?, ?, ?)
	`, genID, chatID, GenerationStatusRunning, "", time.Now().UTC().Add(-1*time.Hour), time.Now().UTC()); err != nil {
		t.Fatalf("failed to seed a generation with an old started_at but fresh heartbeat: %v", err)
	}

	n, err := repo.ReapStaleGenerations(ctx, 5*time.Minute, 5*time.Minute)
	if err != nil {
		t.Fatalf("ReapStaleGenerations failed: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected the fresh-heartbeat generation to survive, but %d rows were reaped", n)
	}

	g, err := repo.GetGeneration(ctx, chatID)
	if err != nil {
		t.Fatalf("GetGeneration failed: %v", err)
	}
	if g.Status != GenerationStatusRunning {
		t.Fatalf("expected the generation to still be running, got %+v", g)
	}
}

func TestGenerationRepository_UpdateGenerationHeartbeat(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()
	genID := uuid.NewString()

	if err := repo.StartGeneration(ctx, genID, chatID, 1); err != nil {
		t.Fatalf("StartGeneration failed: %v", err)
	}

	if err := repo.UpdateGenerationHeartbeat(ctx, genID); err != nil {
		t.Fatalf("UpdateGenerationHeartbeat failed: %v", err)
	}

	var heartbeat sql.NullTime
	if err := conn.QueryRowContext(ctx, `SELECT last_heartbeat_at FROM generations WHERE id = ?`, genID).Scan(&heartbeat); err != nil {
		t.Fatalf("failed to read back last_heartbeat_at: %v", err)
	}
	if !heartbeat.Valid {
		t.Fatalf("expected last_heartbeat_at to be set after UpdateGenerationHeartbeat")
	}
}

func TestService_GenerationStatus(t *testing.T) {
	conn := openTestDB(t)
	svc := &Service{repo: NewRepository(conn)}
	ctx := context.Background()
	chatID := uuid.NewString()

	generating, errMsg := svc.GenerationStatus(ctx, chatID)
	if generating || errMsg != "" {
		t.Fatalf("expected no generation yet, got generating=%v errMsg=%q", generating, errMsg)
	}

	if err := svc.repo.StartGeneration(ctx, uuid.NewString(), chatID, 1); err != nil {
		t.Fatalf("StartGeneration failed: %v", err)
	}
	generating, errMsg = svc.GenerationStatus(ctx, chatID)
	if !generating || errMsg != "" {
		t.Fatalf("expected generating=true while running, got generating=%v errMsg=%q", generating, errMsg)
	}

	if err := svc.repo.EndGeneration(ctx, chatID, fmt.Errorf("something went wrong")); err != nil {
		t.Fatalf("EndGeneration failed: %v", err)
	}
	// EndGeneration sanitizes the error before storing it (see
	// ai.SanitizeError) — errMsg is never the raw error text.
	wantErr := "Error from AI agent: something went wrong while generating a response — please try again in a moment"
	generating, errMsg = svc.GenerationStatus(ctx, chatID)
	if generating || errMsg != wantErr {
		t.Fatalf("expected the failure to be reported, got generating=%v errMsg=%q", generating, errMsg)
	}
}

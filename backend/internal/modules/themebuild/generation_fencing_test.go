package themebuild

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"ai-chat/internal/aicatalog"
	"ai-chat/internal/modules/chat"

	"github.com/google/uuid"
)

// seedReapedThenNext leaves chatID with A reaped (failed) and the next generation B dequeued and running.
func seedReapedThenNext(t *testing.T, conn *sql.DB, repo *Repository, chatID string, tenantID uint64) (staleA, runningB string) {
	t.Helper()
	ctx := context.Background()
	staleA = uuid.NewString()
	startedAt := time.Now().UTC().Add(-10 * time.Minute)
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO generations (id, chat_id, tenant_id, status, attempts, prompt, started_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?)
	`, staleA, chatID, tenantID, GenerationStatusRunning, "", startedAt, startedAt, startedAt); err != nil {
		t.Fatalf("failed to seed stale running generation: %v", err)
	}
	runningB = uuid.NewString()
	if _, err := repo.EnqueueGeneration(ctx, Generation{ID: runningB, ChatID: chatID, TenantID: tenantID, Prompt: "next", ThemeSlug: "test-theme"}); err != nil {
		t.Fatalf("EnqueueGeneration failed: %v", err)
	}

	if _, err := repo.ReapStaleGenerations(ctx, 5*time.Minute, 5*time.Minute); err != nil {
		t.Fatalf("ReapStaleGenerations failed: %v", err)
	}
	if a, err := repo.GetGenerationByID(ctx, chatID, staleA); err != nil || a.Status != GenerationStatusFailed {
		t.Fatalf("expected A reaped to failed, got %+v (err %v)", a, err)
	}
	b, err := repo.DequeueNext(ctx, chatID)
	if err != nil || b.ID != runningB {
		t.Fatalf("expected B dequeued, got %+v (err %v)", b, err)
	}
	return staleA, runningB
}

func TestRepository_StaleWorkerWritesLeaveNextGenerationUntouched(t *testing.T) {
	tests := []struct {
		name  string
		write func(ctx context.Context, conn *sql.DB, repo *Repository, chatID, genID string) error
	}{
		{"EndGeneration success", func(ctx context.Context, _ *sql.DB, repo *Repository, chatID, genID string) error {
			return repo.EndGeneration(ctx, chatID, genID, nil)
		}},
		{"EndGeneration failure", func(ctx context.Context, _ *sql.DB, repo *Repository, chatID, genID string) error {
			return repo.EndGeneration(ctx, chatID, genID, fmt.Errorf("late error"))
		}},
		{"EndGenerationCancelled", func(ctx context.Context, _ *sql.DB, repo *Repository, chatID, genID string) error {
			return repo.EndGenerationCancelled(ctx, chatID, genID)
		}},
		{"SetGenerationAttempts", func(ctx context.Context, _ *sql.DB, repo *Repository, chatID, genID string) error {
			return repo.SetGenerationAttempts(ctx, chatID, genID, 3)
		}},
		{"FinishGenerationTx", func(ctx context.Context, conn *sql.DB, repo *Repository, _, genID string) error {
			tx, err := conn.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer func() { _ = tx.Rollback() }()
			if err := repo.FinishGenerationTx(ctx, tx, genID); err != nil {
				return err
			}
			return tx.Commit()
		}},
		{"LockRunningGenerationTx", func(ctx context.Context, conn *sql.DB, repo *Repository, _, genID string) error {
			tx, err := conn.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer func() { _ = tx.Rollback() }()
			return repo.LockRunningGenerationTx(ctx, tx, genID)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := openTestDB(t)
			repo := NewRepository(conn)
			ctx := context.Background()
			chatID := uuid.NewString()
			staleA, runningB := seedReapedThenNext(t, conn, repo, chatID, 1)

			if err := tt.write(ctx, conn, repo, chatID, staleA); !errors.Is(err, ErrGenerationNotRunning) {
				t.Fatalf("stale write error = %v, want ErrGenerationNotRunning", err)
			}

			b, err := repo.GetGenerationByID(ctx, chatID, runningB)
			if err != nil {
				t.Fatalf("GetGenerationByID failed: %v", err)
			}
			if b.Status != GenerationStatusRunning || b.Error != nil || b.FinishedAt != nil || b.Attempts != 0 {
				t.Fatalf("B was modified by A's stale write: %+v", b)
			}
			a, err := repo.GetGenerationByID(ctx, chatID, staleA)
			if err != nil {
				t.Fatalf("GetGenerationByID failed: %v", err)
			}
			if a.Status != GenerationStatusFailed || a.Error == nil || *a.Error != "generation timed out (reaped)" || a.Attempts != 0 {
				t.Fatalf("A's reaped outcome was overwritten: %+v", a)
			}

			if err := repo.EndGeneration(ctx, chatID, runningB, nil); err != nil {
				t.Fatalf("B's own EndGeneration failed: %v", err)
			}
		})
	}
}

func TestRepository_UpdateGenerationHeartbeat_IgnoresFinishedRow(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()
	staleA, runningB := seedReapedThenNext(t, conn, repo, chatID, 1)

	if err := repo.UpdateGenerationHeartbeat(ctx, staleA); err != nil {
		t.Fatalf("UpdateGenerationHeartbeat failed: %v", err)
	}
	var heartbeat sql.NullTime
	if err := conn.QueryRowContext(ctx, `SELECT last_heartbeat_at FROM generations WHERE id = ?`, staleA).Scan(&heartbeat); err != nil {
		t.Fatalf("failed to read last_heartbeat_at: %v", err)
	}
	if heartbeat.Valid {
		t.Fatalf("a reaped generation's heartbeat must not be stamped, got %v", heartbeat.Time)
	}
	if err := repo.EndGeneration(ctx, chatID, runningB, nil); err != nil {
		t.Fatalf("B's own EndGeneration failed: %v", err)
	}
}

func TestService_RecordFailedTurn_SkipsReapedGeneration(t *testing.T) {
	conn := openTestDB(t)
	chatSvc := chat.NewService(chat.NewRepository(conn))
	repo := NewRepository(conn)
	svc := &Service{repo: repo, chats: chatSvc}
	ctx := context.Background()
	tenantID := uint64(time.Now().UnixNano())
	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}
	staleA, runningB := seedReapedThenNext(t, conn, repo, c.ID, tenantID)

	if err := svc.recordFailedTurn(ctx, c, staleA, "late failure", aicatalog.Choice{}); !errors.Is(err, ErrGenerationNotRunning) {
		t.Fatalf("recordFailedTurn for reaped A = %v, want ErrGenerationNotRunning", err)
	}
	if err := svc.recordFailedTurn(ctx, c, runningB, "B failed", aicatalog.Choice{}); err != nil {
		t.Fatalf("recordFailedTurn for running B failed: %v", err)
	}

	messages, err := chatSvc.ListMessagesForVerifiedChat(ctx, c.ID)
	if err != nil {
		t.Fatalf("ListMessagesForVerifiedChat failed: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != "B failed" {
		t.Fatalf("expected only B's failure message, got %+v", messages)
	}
	if err := repo.EndGeneration(ctx, c.ID, runningB, fmt.Errorf("B failed")); err != nil {
		t.Fatalf("B's own EndGeneration failed: %v", err)
	}
}

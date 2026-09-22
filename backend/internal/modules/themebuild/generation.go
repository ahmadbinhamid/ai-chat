package themebuild

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"ai-chat/internal/modules/chat"
	"ai-chat/internal/safego"
)

// Durable row surviving pod restart and working across replicas.
type Generation struct {
	ID       string
	ChatID   string
	TenantID uint64
	Status   string // "queued" | "running" | "succeeded" | "failed" | "cancelled"
	Error    *string
	Attempts int
	// Set at enqueue time, read back by runGeneration on dequeue. The bearer token is
	// deliberately NOT one of these fields — see pendingTokens' doc comment in service.go.
	Prompt        string
	UserMessageID *string
	ThemeSlug     string
	Mode          string
	// ReferenceURL is a URL found in Prompt at enqueue time; the actual fetch happens in
	// doGenerate once dequeued, not synchronously in the enqueuing HTTP request.
	ReferenceURL string
	// QueuedAt is nil only for a row seeded directly as "running" (existing tests).
	QueuedAt *time.Time
	// StartedAt is nil until DequeueNext promotes this row to running.
	StartedAt  *time.Time
	FinishedAt *time.Time
}

const (
	GenerationStatusQueued    = "queued"
	GenerationStatusRunning   = "running"
	GenerationStatusSucceeded = "succeeded"
	GenerationStatusFailed    = "failed"
	GenerationStatusCancelled = "cancelled"
)

type rowScanner interface {
	Scan(dest ...any) error
}

// Reads from *sql.Row or *sql.Rows; centralizes nullable-column handling.
func scanGeneration(s rowScanner) (Generation, error) {
	var g Generation
	var errMsg, userMessageID, referenceURL sql.NullString
	var queuedAt, startedAt, finishedAt sql.NullTime

	err := s.Scan(&g.ID, &g.ChatID, &g.TenantID, &g.Status, &errMsg, &g.Attempts,
		&g.Prompt, &referenceURL, &userMessageID, &g.ThemeSlug, &g.Mode, &queuedAt, &startedAt, &finishedAt)
	if err != nil {
		return Generation{}, err
	}
	if errMsg.Valid {
		g.Error = &errMsg.String
	}
	if referenceURL.Valid {
		g.ReferenceURL = referenceURL.String
	}
	if userMessageID.Valid {
		g.UserMessageID = &userMessageID.String
	}
	if queuedAt.Valid {
		g.QueuedAt = &queuedAt.Time
	}
	if startedAt.Valid {
		g.StartedAt = &startedAt.Time
	}
	if finishedAt.Valid {
		g.FinishedAt = &finishedAt.Time
	}
	return g, nil
}

// Balance: tight enough for responsiveness, loose enough not to affect load.
const reaperInterval = 1 * time.Minute

// Sweeps immediately, then every reaperInterval; wrapped per-call for panic isolation.
func (s *Service) RunReaper(ctx context.Context) {
	safeReapOnce := func() {
		defer safego.Recover("themebuild.reapOnce")
		s.reapOnce(ctx)
	}

	safeReapOnce()

	ticker := time.NewTicker(reaperInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			safeReapOnce()
		}
	}
}

// Longer than reaperInterval, shorter than generateTimeout.
const generationHeartbeatTimeout = 5 * time.Minute

func (s *Service) reapOnce(ctx context.Context) {
	n, err := s.repo.ReapStaleGenerations(ctx, generationHeartbeatTimeout, generateTimeout())
	if err != nil {
		slog.Error("reap stale generations failed", "error", err)
	} else if n > 0 {
		slog.Warn("reaped stale generations", "count", n)
	}

	s.reapOrphanedQueues(ctx)
}

// Fails chats with queues stranded by dead pod (no drain loop or bearer token).
func (s *Service) reapOrphanedQueues(ctx context.Context) {
	chatIDs, err := s.repo.ChatsWithOrphanedQueues(ctx)
	if err != nil {
		slog.Error("failed to list chats with orphaned queues", "error", err)
		return
	}
	for _, chatID := range chatIDs {
		s.failOrphanedQueue(ctx, chatID)
	}
}

// Fails entire queue, not just first row, so later prompts aren't silently left behind.
func (s *Service) failOrphanedQueue(ctx context.Context, chatID string) {
	for {
		g, err := s.repo.DequeueNext(ctx, chatID)
		if errors.Is(err, ErrNotFound) {
			return
		}
		if errors.Is(err, ErrGenerationInProgress) {
			// A live pod claimed the running slot since ChatsWithOrphanedQueues' read — no longer orphaned.
			return
		}
		if err != nil {
			slog.Error("failed to dequeue an orphaned generation", "chat_id", chatID, "error", err)
			return
		}

		// Built from row, not looked up: reaper has no tenant-scoped request.
		c := chat.Chat{ID: chatID, TenantID: g.TenantID}
		// Warn: expected, but spike could indicate false staleness detection.
		slog.Warn("failing an orphaned queued generation with session-expired", "chat_id", chatID, "generation_id", g.ID)
		s.recordGenerationFailure(ctx, c, g.ID, errSessionExpired)
		if endErr := s.repo.EndGeneration(ctx, chatID, errSessionExpired); endErr != nil {
			slog.Error("failed to record generation end for an orphaned queue", "chat_id", chatID, "error", endErr)
		}
	}
}

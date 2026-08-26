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

// Generation is one row of the generations table — a single background
// Generate call's durable state (phase 3a), replacing the old in-memory
// generationTracker so it survives a pod restart and works across more
// than one replica. Extended (see the queueing brief) to double as a
// queued row's own memory of what it was asked to do: a queued generation
// can sit for a while before DequeueNext promotes it, well after the HTTP
// request that created it is gone, so Prompt/ThemeSlug/Mode/UserMessageID
// have to be readable back off the row itself rather than carried in a
// captured GenerateInput (see Service.Generate/runGeneration).
type Generation struct {
	ID       string
	ChatID   string
	TenantID uint64
	Status   string // "queued" | "running" | "succeeded" | "failed" | "cancelled"
	Error    *string
	Attempts int
	// Prompt/UserMessageID/ThemeSlug/Mode are set at enqueue time and read
	// back by runGeneration when this row is dequeued. UserMessageID points
	// at the chat_messages row RecordUserMessage already wrote when the
	// prompt was accepted (see Generate) — nil only for rows seeded outside
	// the normal Generate path (existing tests using StartGeneration
	// directly). The bearer token needed to actually run this generation is
	// deliberately NOT one of these fields — see pendingTokens' doc comment
	// in service.go for why it never touches this table.
	Prompt        string
	UserMessageID *string
	ThemeSlug     string
	Mode          string
	// QueuedAt is nil for a row seeded directly as "running" (existing
	// tests) and set for every row that ever went through EnqueueGeneration.
	QueuedAt *time.Time
	// StartedAt is nil until DequeueNext promotes this row to running —
	// a queued row hasn't started yet, so this can no longer be a plain
	// time.Time (see the 20260812000001 migration).
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

// scanGeneration reads one generationColumns row from either a *sql.Row or
// mid-iteration *sql.Rows (both satisfy rowScanner) — shared by every
// method in this file that returns a Generation, so the nullable-column
// handling (error/user_message_id/queued_at/started_at/finished_at) is
// written exactly once.
func scanGeneration(s rowScanner) (Generation, error) {
	var g Generation
	var errMsg, userMessageID sql.NullString
	var queuedAt, startedAt, finishedAt sql.NullTime

	err := s.Scan(&g.ID, &g.ChatID, &g.TenantID, &g.Status, &errMsg, &g.Attempts,
		&g.Prompt, &userMessageID, &g.ThemeSlug, &g.Mode, &queuedAt, &startedAt, &finishedAt)
	if err != nil {
		return Generation{}, err
	}
	if errMsg.Valid {
		g.Error = &errMsg.String
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

// reaperInterval is how often RunReaper sweeps for stale generations —
// matches the brief's "every minute", tight enough that a merchant polling
// GET /chat isn't stuck on a dead generation for long, loose enough not to
// matter as load.
const reaperInterval = 1 * time.Minute

// RunReaper sweeps for and fails stale generations immediately, then every
// reaperInterval until ctx is canceled (see server.Close). Runs once at
// startup in addition to the ticker so a generation orphaned by a crash
// right before this process restarted doesn't wait a full interval.
func (s *Service) RunReaper(ctx context.Context) {
	// Each call wrapped individually (not once for RunReaper as a whole):
	// this is launched via a bare `go` in server.go and runs for the
	// process's entire lifetime — a panic in one sweep recovering shouldn't
	// end reaping for good afterward, since that's the exact mechanism
	// every other orphan-recovery path in this package (a crashed pod, a
	// lost bearer token) depends on staying alive.
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

// generationHeartbeatTimeout is how long a running generation may go
// without a heartbeat (see eventEmitter.emit) before the reaper presumes
// its process died — comfortably longer than reaperInterval (1 minute) so
// one missed sweep doesn't false-positive a generation that's simply
// between progress events, short enough that a genuinely stuck generation
// no longer has to wait out the full generateTimeout budget (up to 65
// minutes) before ReapStaleGenerations touches it.
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

// reapOrphanedQueues restarts any chat whose queue was stranded by a dead
// pod: nothing running for that chat means no process anywhere has a
// runGeneration drain loop alive to ever dequeue its queued rows again
// (see ChatsWithOrphanedQueues). Actually resuming those prompts would need
// a bearer token, and this reaper — like any process that didn't originally
// accept the HTTP request — has none (see pendingTokens' doc comment in
// service.go). Properly resuming them would need a service-account
// credential distinct from any one merchant's session, which is a larger
// design decision than this feature makes; until that exists, this fails
// every orphaned row with the same session-expired message a normal queued
// row gets when its own token is missing, so the merchant sees a clear
// "send it again" instead of a prompt that silently never runs.
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

// failOrphanedQueue drains chatID's entire stranded queue, failing each row
// in turn — not just the first one — so a chat with several queued prompts
// doesn't have its later prompts silently left behind once the first is
// dealt with.
func (s *Service) failOrphanedQueue(ctx context.Context, chatID string) {
	for {
		g, err := s.repo.DequeueNext(ctx, chatID)
		if errors.Is(err, ErrNotFound) {
			return
		}
		if errors.Is(err, ErrGenerationInProgress) {
			// A live pod claimed the running slot between
			// ChatsWithOrphanedQueues' read and now — this chat isn't
			// actually orphaned anymore, its own drain loop owns the rest.
			return
		}
		if err != nil {
			slog.Error("failed to dequeue an orphaned generation", "chat_id", chatID, "error", err)
			return
		}

		// Built directly from the generation row rather than looked up
		// through chat.Service: RecordAssistantMessage only ever reads
		// c.ID/c.TenantID, and a reaper sweep has no tenant-scoped request
		// to look the chat up through in the first place.
		c := chat.Chat{ID: chatID, TenantID: g.TenantID}
		// Warn (not Error): an orphaned queue is an expected, already-
		// handled condition on its own (see this method's own doc comment)
		// — but see runOneQueuedGeneration's matching log line for why a
		// SPIKE in this message specifically is worth being able to spot:
		// before the heartbeat fix, a chat's running generation going
		// falsely stale (see the 20260813000002 migration) is exactly what
		// made ChatsWithOrphanedQueues see this chat as orphaned in the
		// first place, despite its running generation completing normally
		// moments later.
		slog.Warn("failing an orphaned queued generation with session-expired", "chat_id", chatID, "generation_id", g.ID)
		s.recordGenerationFailure(ctx, c, g.ID, errSessionExpired)
		if endErr := s.repo.EndGeneration(ctx, chatID, errSessionExpired); endErr != nil {
			slog.Error("failed to record generation end for an orphaned queue", "chat_id", chatID, "error", endErr)
		}
	}
}

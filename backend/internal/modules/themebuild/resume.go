package themebuild

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"ai-chat/internal/modules/chat"
	"ai-chat/internal/safego"
)

// resumeWindow is how long a generation left queued by a restart waits for its sender before failing.
const resumeWindow = 10 * time.Minute

var (
	errResumeExpired = errors.New("the server was updated while this message was waiting, and it couldn't " +
		"continue without you — send it again")
	errInterruptedTwice = errors.New("this message was interrupted by two server updates in a row — send it again")
)

const resumedAfterUpdateNote = "Note: this turn was paused by a server update and resumed when you came back."

// waitingForSender: a restart left g queued without its token, and its sender may still come back for it.
func waitingForSender(g Generation) bool {
	return g.AwaitingResumeSince != nil && time.Since(*g.AwaitingResumeSince) < resumeWindow
}

// PrepareResume runs at startup: every queued generation lost its sender's token with the old process, so each now
// waits for that user's next request. Errors are logged; the reaper then fails what it can't resume.
func (s *Service) PrepareResume(ctx context.Context) {
	n, err := s.repo.MarkQueuedAwaitingResume(ctx, time.Now().UTC())
	if err != nil {
		slog.Error("failed to mark queued generations awaiting their senders", "error", err)
		return
	}
	s.resumePending.Store(n > 0)
	if n > 0 {
		slog.Info("queued generations are waiting for their senders after a restart", "count", n, "window", resumeWindow)
	}
}

// RequeueUnfinished runs when a shutdown drain hits its limit: generations still running go back in the queue to
// resume after the restart, once; one already resumed before fails with a clear message instead.
func (s *Service) RequeueUnfinished(ctx context.Context) (requeued, failed int) {
	for genID, c := range s.runs.runningGenerations() {
		ok, err := s.repo.RequeueInterrupted(ctx, genID)
		if err != nil {
			slog.Error("failed to re-queue an interrupted generation", "generation_id", genID, "error", err)
			continue
		}
		if ok {
			requeued++
			continue
		}
		g, err := s.repo.GetGenerationByID(ctx, c.ID, genID)
		if err != nil || g.Status != GenerationStatusRunning {
			continue // finished in the meantime
		}
		s.recordGenerationFailure(ctx, c, genID, errInterruptedTwice)
		if err := s.repo.EndGeneration(ctx, c.ID, errInterruptedTwice); err != nil {
			slog.Error("failed to record generation end", "chat_id", c.ID, "error", err)
		}
		failed++
	}
	return requeued, failed
}

// ResumeForUser gives a returning user's fresh token to the generations they sent that a restart left waiting, and
// restarts those chats' queues. Only the sender's token is ever used, never another user's in the same store.
func (s *Service) ResumeForUser(ctx context.Context, tenantID uint64, userID *uint64, token string) {
	if !s.resumePending.Load() || userID == nil || token == "" {
		return
	}
	waiting, err := s.repo.AwaitingResumeForUser(ctx, tenantID, *userID)
	if err != nil {
		slog.Error("failed to list generations awaiting their sender", "tenant_id", tenantID, "error", err)
		return
	}
	chats := make(map[string]bool, len(waiting))
	for _, w := range waiting {
		s.tokens.store(w.GenerationID, token)
		chats[w.ChatID] = true
	}
	for chatID := range chats {
		started, err := s.startChatQueue(ctx, chat.Chat{ID: chatID, TenantID: tenantID})
		if err != nil && !errors.Is(err, ErrGenerationInProgress) && !errors.Is(err, ErrNotFound) {
			slog.Error("failed to resume a chat's queue", "chat_id", chatID, "error", err)
		}
		if started {
			slog.Info("resuming generations for their returning sender", "chat_id", chatID, "tenant_id", tenantID)
		}
	}
}

// refreshResumePending turns the per-request resume check off once nothing is waiting any more.
func (s *Service) refreshResumePending(ctx context.Context) {
	if !s.resumePending.Load() {
		return
	}
	n, err := s.repo.CountAwaitingResume(ctx)
	if err == nil && n == 0 {
		s.resumePending.Store(false)
	}
}

// startChatQueue starts a drain loop on the chat's oldest queued generation. started is false while draining.
func (s *Service) startChatQueue(ctx context.Context, c chat.Chat) (started bool, err error) {
	if !s.runs.start() {
		return false, nil
	}
	next, err := s.repo.DequeueNext(ctx, c.ID)
	if err != nil {
		s.runs.done()
		return false, err
	}
	// Detached from request lifecycle, but bounded: each iteration gets its own generateTimeout.
	go func() {
		defer s.runs.done()
		defer safego.Recover("themebuild.runGeneration")
		s.runGeneration(context.WithoutCancel(ctx), c, next)
	}()
	return true, nil
}

// releaseToQueue hands a dequeued generation back, keeping its place, for its sender's return to pick up.
func (s *Service) releaseToQueue(ctx context.Context, c chat.Chat, g Generation) {
	if err := s.repo.ReleaseClaim(ctx, g.ID); err != nil {
		slog.Error("failed to release a generation waiting for its sender", "chat_id", c.ID, "generation_id", g.ID, "error", err)
	}
}

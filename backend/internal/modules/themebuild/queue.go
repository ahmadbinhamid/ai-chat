package themebuild

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"
)

// Position computed at read time, not stored (relative to queue order only).
type PendingGeneration struct {
	GenerationID  string `json:"generation_id"`
	Status        string `json:"status"`
	PromptPreview string `json:"prompt_preview"`
	Position      int    `json:"position"`
}

func (s *Service) ListPendingGenerations(ctx context.Context, chatID string) ([]PendingGeneration, error) {
	gens, err := s.repo.ListPending(ctx, chatID)
	if err != nil {
		return nil, err
	}
	out := make([]PendingGeneration, len(gens))
	for i, g := range gens {
		out[i] = PendingGeneration{
			GenerationID:  g.ID,
			Status:        g.Status,
			PromptPreview: PromptPreview(g.Prompt),
			Position:      i,
		}
	}
	return out, nil
}

// Queued rows stop synchronously; running rows get live stop request, returns once sent.
func (s *Service) CancelQueuedGeneration(ctx context.Context, tenantID uint64, chatID, generationID string) error {
	if _, err := s.chats.GetChat(ctx, tenantID, chatID); err != nil {
		return err
	}
	g, err := s.repo.GetGenerationByID(ctx, chatID, generationID)
	if err != nil {
		return err
	}

	switch g.Status {
	case GenerationStatusQueued:
		return s.cancelOneQueued(ctx, chatID, generationID)
	case GenerationStatusRunning:
		return s.cancelOneRunning(ctx, chatID, generationID)
	default:
		slog.Warn("cancel requested for a generation that's neither queued nor running", "chat_id", chatID, "generation_id", generationID, "status", g.Status)
		return ErrNotFound
	}
}

// Bounds CancelAllPending's retry loop to handle mid-pass promotion races.
const maxCancelAllPasses = 5

// Cancels running and queued generations in bounded passes; handles mid-pass promotions.
func (s *Service) CancelAllPending(ctx context.Context, tenantID uint64, chatID string) error {
	if _, err := s.chats.GetChat(ctx, tenantID, chatID); err != nil {
		return err
	}

	// Detached ctx: must complete even if caller disconnects.
	cancelCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var firstErr error
	record := func(generationID, status string, cancelErr error) {
		if cancelErr == nil {
			return
		}
		if errors.Is(cancelErr, ErrNotFound) {
			// Expected in retry loop: status changed between ListPending read and cancel write.
			return
		}
		if firstErr == nil {
			firstErr = cancelErr
		}
		slog.Error("failed to cancel one generation as part of cancel-all", "chat_id", chatID, "generation_id", generationID, "status", status, "error", cancelErr)
	}

	for pass := 0; pass < maxCancelAllPasses; pass++ {
		pending, err := s.repo.ListPending(cancelCtx, chatID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return firstErr
		}
		if len(pending) == 0 {
			return firstErr
		}

		// Cancel queued first: running row's stop can promote next queued within milliseconds.
		var runningID string
		for _, g := range pending {
			if g.Status == GenerationStatusRunning {
				runningID = g.ID
				continue
			}
			record(g.ID, g.Status, s.cancelOneQueued(cancelCtx, chatID, g.ID))
		}
		if runningID != "" {
			record(runningID, GenerationStatusRunning, s.cancelOneRunning(cancelCtx, chatID, runningID))
		}
	}
	return firstErr
}

func (s *Service) cancelOneQueued(ctx context.Context, chatID, generationID string) error {
	if err := s.repo.CancelQueued(ctx, chatID, generationID); err != nil {
		return err
	}
	// Drop token so pendingTokens doesn't hold it until process exit.
	s.tokens.discard(generationID)

	emitter := newEventEmitter(ctx, s.repo, s.bus, generationID, chatID)
	emitter.emit(ctx, EventTypeCancelled, map[string]string{"generation_id": generationID})
	return nil
}

func (s *Service) cancelOneRunning(ctx context.Context, chatID, generationID string) error {
	// Durable write FIRST: live publish only reaches registered subscribers; heartbeat is backstop.
	if err := s.repo.RequestGenerationCancellation(ctx, chatID, generationID); err != nil {
		return err
	}
	slog.Info("cancel requested for a running generation", "chat_id", chatID, "generation_id", generationID)

	payload, err := json.Marshal(map[string]string{"generation_id": generationID})
	if err != nil {
		return err
	}
	// Publish directly to bus: stop request must never be replayed; write above is durable.
	s.bus.Publish(ctx, chatID, GenerationEvent{
		GenerationID: generationID,
		ChatID:       chatID,
		Type:         EventTypeCancelRequested,
		Payload:      payload,
		CreatedAt:    time.Now().UTC(),
	})
	return nil
}

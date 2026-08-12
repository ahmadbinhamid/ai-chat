package themebuild

import "context"

// PendingGeneration is one entry of a chat's pending queue — the running
// generation (if any) plus every generation still waiting behind it, in the
// order they'll actually run. Position is computed here (not stored on the
// row) since it only ever makes sense relative to the rest of the list at
// read time.
type PendingGeneration struct {
	GenerationID  string `json:"generation_id"`
	Status        string `json:"status"`
	PromptPreview string `json:"prompt_preview"`
	Position      int    `json:"position"`
}

// ListPendingGenerations returns chatID's running generation (if any) plus
// every queued one behind it, oldest first, each annotated with its 0-based
// position — for GET /chat and the stream's initial state (see
// Repository.ListPending's doc comment for the ordering guarantee this
// relies on).
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

// CancelQueuedGeneration cancels chatID's queued generation generationID,
// after verifying chatID belongs to tenantID — the same ownership pattern
// RevertToMessage uses. Never touches a running row (see
// Repository.CancelQueued's doc comment): cancelling a generationID that
// names a running or already-finished row, or one that doesn't exist at
// all, surfaces as the same ErrNotFound either way — there's nothing
// meaningful to tell those cases apart for the caller here.
func (s *Service) CancelQueuedGeneration(ctx context.Context, tenantID uint64, chatID, generationID string) error {
	if _, err := s.chats.GetChat(ctx, tenantID, chatID); err != nil {
		return err
	}
	if err := s.repo.CancelQueued(ctx, chatID, generationID); err != nil {
		return err
	}

	// This generation will never be dequeued now — drop its token so
	// pendingTokens doesn't hold it until process exit (see its doc
	// comment; take() already does this for the normal run-to-completion
	// path, this is the equivalent cleanup for the cancel path).
	s.tokens.discard(generationID)

	emitter := newEventEmitter(ctx, s.repo, s.bus, generationID, chatID)
	emitter.emit(ctx, EventTypeCancelled, struct{}{})
	return nil
}

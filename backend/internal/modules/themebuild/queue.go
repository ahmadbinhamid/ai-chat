package themebuild

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"
)

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

// CancelQueuedGeneration cancels chatID's generation generationID, after
// verifying chatID belongs to tenantID — the same ownership pattern
// RevertToMessage uses. Branches on the row's current status:
//   - queued: stopped immediately here, synchronously (see
//     Repository.CancelQueued).
//   - running: this process isn't necessarily the one running it (see
//     eventBus's Redis-vs-in-process doc comment), so it can only publish
//     a request — EventTypeCancelRequested, live-only, never durably
//     stored — and let whichever goroutine actually owns generationID
//     notice and stop itself (see runOneQueuedGeneration). The caller
//     sees this call succeed once the request is sent, not once the
//     generation has actually stopped; the terminal EventTypeCancelled
//     (and the row's status flipping to cancelled) follows shortly after,
//     same as any other generation outcome.
//   - anything else (already finished, or generationID doesn't exist at
//     all): ErrNotFound — there's nothing meaningful to tell those cases
//     apart for the caller here.
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

// maxCancelAllPasses bounds CancelAllPending's retry loop — see its doc
// comment for the race a second (or third) pass exists to close. One
// retry closes the race in the overwhelmingly common case (at most one
// generation can realistically finish and get replaced in the time this
// method takes to run); the rest are cheap insurance against a
// pathological run of near-instant generations, not something normal
// traffic should ever need.
const maxCancelAllPasses = 5

// CancelAllPending cancels every generation chatID has waiting — the
// running one (if any) plus everything still queued behind it — in one
// call, rather than leaving the merchant to cancel the running one and
// then watch the next queued prompt immediately take its place. Verifies
// chatID belongs to tenantID the same way CancelQueuedGeneration does.
//
// Runs as a bounded sequence of passes (see maxCancelAllPasses), not a
// single one, over ListPending()/cancel/repeat. A single pass has a real
// gap: nothing about this method's own cancellation is what usually
// exposes it (queued rows are cancelled before the running one specifically
// to close THAT version of the race — see cancelOneQueued's call site
// below), but the originally-running generation can just as well finish
// entirely on its own — success, failure, or its own timeout — via
// runGeneration's independent per-chat drain-loop goroutine, completely
// unrelated to anything this method does. The instant that happens,
// DequeueNext promotes the next queued row to running. If that lands
// while this method's current pass is still iterating the queued rows it
// snapshotted earlier, the promoted row's cancelOneQueued call misses
// (already running, not queued) and the pass's own cancelOneRunning call
// also misses (targets the original, now-finished running id) — that row
// would be left running, untouched, for good. A follow-up pass sees the
// world fresh: whatever got promoted is now visible as "running" in its
// own ListPending call and gets cancelled correctly. The loop stops the
// moment a pass finds nothing left pending.
//
// Best-effort within each pass: one row failing to cancel (e.g. it
// finished a moment before this ran) doesn't stop the rest from being
// attempted — this mirrors runGeneration's own "one failure doesn't block
// the rest of the queue" stance, just for cancellation instead of
// completion. Returns the first error encountered across every pass, if
// any, purely for logging/visibility; a partial cancel still leaves the
// chat in a safe, self-consistent state (whatever didn't cancel just
// keeps running/queued normally, and the next merchant action or the
// reaper still applies as usual).
func (s *Service) CancelAllPending(ctx context.Context, tenantID uint64, chatID string) error {
	if _, err := s.chats.GetChat(ctx, tenantID, chatID); err != nil {
		return err
	}

	// Detached from ctx (the caller's own HTTP request context) for every
	// pass below, the same reasoning as doGenerate's commitCtx: a "cancel
	// everything" request that's already been accepted (ownership already
	// verified above) should reliably run to completion once started, not
	// stop partway through a multi-row, multi-pass loop just because the
	// merchant's tab closed or the request timed out. 30s is generous for
	// maxCancelAllPasses passes over a queue bounded by maxQueueDepth.
	cancelCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var firstErr error
	record := func(generationID, status string, cancelErr error) {
		if cancelErr == nil {
			return
		}
		if errors.Is(cancelErr, ErrNotFound) {
			// Expected in a multi-pass loop, not a failure: this row's
			// status moved out from under THIS specific request between
			// ListPending's read and the cancel write — either an
			// earlier pass already requested/completed its cancellation
			// (cancelOneRunning can legitimately be called more than once
			// across passes while the async stop is still landing — see
			// its own doc comment), or it finished/failed on its own.
			// Either way nothing is left to do for this id; the next
			// pass's fresh ListPending reflects whatever's actually true
			// now, and the loop's own len(pending)==0 exit is what
			// notices there's nothing left to retry.
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

		// Queued rows cancelled before the running one, deliberately,
		// never the reverse: cancelling the running generation only
		// requests a stop (cancelOneRunning is asynchronous — see its own
		// doc comment), and that stop can land within milliseconds once
		// the listener goroutine picks up the live signal — immediately
		// freeing the drain loop to promote whatever's still queued.
		// Cancelling every queued row in THIS pass first means there's
		// nothing left for that specific promotion to find.
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

// cancelOneQueued stops one still-queued generation synchronously — see
// CancelQueuedGeneration's doc comment for the queued case.
func (s *Service) cancelOneQueued(ctx context.Context, chatID, generationID string) error {
	if err := s.repo.CancelQueued(ctx, chatID, generationID); err != nil {
		return err
	}
	// This generation will never be dequeued now — drop its token so
	// pendingTokens doesn't hold it until process exit (see its doc
	// comment; take() already does this for the normal run-to-completion
	// path, this is the equivalent cleanup for the cancel path).
	s.tokens.discard(generationID)

	emitter := newEventEmitter(ctx, s.repo, s.bus, generationID, chatID)
	emitter.emit(ctx, EventTypeCancelled, map[string]string{"generation_id": generationID})
	return nil
}

// cancelOneRunning requests one running generation to stop — see
// CancelQueuedGeneration's doc comment for the running case (durable write
// first, live signal second, asynchronous).
func (s *Service) cancelOneRunning(ctx context.Context, chatID, generationID string) error {
	// Durable write FIRST, live signal second: the live-only
	// EventTypeCancelRequested publish below only reaches a subscriber
	// already registered at the moment it's sent — a request landing in
	// the gap between DequeueNext marking this row running and
	// runOneQueuedGeneration's own goroutine reaching its Subscribe call
	// would otherwise be silently lost. This UPDATE is what
	// runOneQueuedGeneration checks back (immediately after subscribing,
	// then again on every heartbeat tick as a backstop for a dropped live
	// event too — see the 20260831000001 migration's doc comment) so a
	// cancel request always eventually lands even when the live path
	// misses it.
	if err := s.repo.RequestGenerationCancellation(ctx, chatID, generationID); err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]string{"generation_id": generationID})
	if err != nil {
		return err
	}
	// Straight to the bus, not through eventEmitter.emit: this is a
	// request to stop, not a progress event of its own — it must never
	// consume a durable seq number or be replayed to a reconnecting
	// client (see EventTypeCancelRequested's doc comment). Purely the
	// fast path now that the write above is durable — its own delivery
	// can stay best-effort.
	s.bus.Publish(ctx, chatID, GenerationEvent{
		GenerationID: generationID,
		ChatID:       chatID,
		Type:         EventTypeCancelRequested,
		Payload:      payload,
		CreatedAt:    time.Now().UTC(),
	})
	return nil
}

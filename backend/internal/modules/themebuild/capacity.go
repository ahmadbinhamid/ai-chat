package themebuild

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"ai-chat/internal/genlimit"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/safego"
)

// capacityWaitHeartbeat keeps a waiting chat's queued rows fresh for ChatsWithOrphanedQueues; well under
// generationHeartbeatTimeout.
const capacityWaitHeartbeat = 30 * time.Second

// errAwaitingCapacity: startChatQueue started a loop that waits, with the chat's rows still queued, for a slot.
var errAwaitingCapacity = errors.New("waiting for generation capacity")

var (
	errNothingQueued = errors.New("chat has nothing left queued")
	errDrainingWait  = errors.New("draining; leaving the queue for the next process")
)

// SetGenerationLimits caps concurrent generations globally and per tenant; 0 is unlimited.
func (s *Service) SetGenerationLimits(global, perTenant int) {
	s.limiter = genlimit.New(global, perTenant)
}

// chatSet holds the chats with a loop waiting for capacity; entries leave when the wait ends, so it never outgrows
// the waits in flight.
type chatSet struct {
	mu    sync.Mutex
	chats map[string]struct{}
}

func (cs *chatSet) add(chatID string) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if _, ok := cs.chats[chatID]; ok {
		return false
	}
	if cs.chats == nil {
		cs.chats = make(map[string]struct{})
	}
	cs.chats[chatID] = struct{}{}
	return true
}

func (cs *chatSet) remove(chatID string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	delete(cs.chats, chatID)
}

// claimNext takes a slot (waiting if needed) and only then dequeues: a dequeued row is running, and the reaper fails
// a running row that waits past generationHeartbeatTimeout. ErrGenerationInProgress: another loop owns the queue.
func (s *Service) claimNext(ctx context.Context, c chat.Chat) (Generation, func(), error) {
	if release, ok := s.limiter.TryAcquire(c.TenantID); ok {
		return s.dequeueWith(ctx, c, release)
	}
	if !s.waiters.add(c.ID) {
		return Generation{}, nil, ErrGenerationInProgress
	}
	return s.claimAfterWait(ctx, c, true)
}

// claimAfterWait waits for a slot and dequeues; the caller has registered c in s.waiters, which this releases.
func (s *Service) claimAfterWait(ctx context.Context, c chat.Chat, emitQueued bool) (Generation, func(), error) {
	defer s.waiters.remove(c.ID)
	release, err := s.awaitCapacity(ctx, c, emitQueued)
	if err != nil {
		return Generation{}, nil, err
	}
	return s.dequeueWith(ctx, c, release)
}

func (s *Service) dequeueWith(ctx context.Context, c chat.Chat, release func()) (Generation, func(), error) {
	g, err := s.repo.DequeueNext(ctx, c.ID)
	if err != nil {
		release()
		return Generation{}, nil, err
	}
	return g, release, nil
}

// awaitCapacity blocks until a slot frees, ctx ends, draining begins, or the chat has nothing left queued (a user
// cancel). The chat's queued rows are heartbeated meanwhile so no replica's reaper takes them for orphans.
func (s *Service) awaitCapacity(ctx context.Context, c chat.Chat, emitQueued bool) (func(), error) {
	waitCtx, stop := context.WithCancelCause(ctx)
	defer stop(nil)

	if !s.touchQueued(waitCtx, c) {
		return nil, errNothingQueued
	}
	slog.Info("generation capacity full; waiting with the chat's prompts still queued", "chat_id", c.ID, "tenant_id", c.TenantID)
	if emitQueued {
		s.emitWaitingForCapacity(waitCtx, c)
	}

	events, unsubscribe := s.bus.Subscribe(context.Background(), c.ID)
	defer unsubscribe()
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		defer safego.Recover("themebuild.capacityWaitWatch")
		tick := time.NewTicker(capacityWaitHeartbeat)
		defer tick.Stop()
		for {
			select {
			case <-watchDone:
				return
			case <-s.runs.drainedCh():
				stop(errDrainingWait)
				return
			case <-tick.C:
			case ev, ok := <-events:
				if !ok {
					events = nil
					continue
				}
				if ev.Type != EventTypeCancelled {
					continue
				}
			}
			if !s.touchQueued(waitCtx, c) {
				stop(errNothingQueued)
				return
			}
		}
	}()

	release, err := s.limiter.Acquire(waitCtx, c.TenantID)
	if err != nil {
		return nil, context.Cause(waitCtx)
	}
	return release, nil
}

// touchQueued is false only when the chat has nothing queued; a DB error keeps the wait going.
func (s *Service) touchQueued(ctx context.Context, c chat.Chat) bool {
	n, err := s.repo.TouchQueuedHeartbeat(ctx, c.ID)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("failed to heartbeat queued generations while waiting for capacity", "chat_id", c.ID, "error", err)
		}
		return true
	}
	return n > 0
}

// emitWaitingForCapacity sends one "queued" event for the chat's next prompt, so the UI shows it waiting, not stuck.
func (s *Service) emitWaitingForCapacity(ctx context.Context, c chat.Chat) {
	pending, err := s.repo.ListPending(ctx, c.ID)
	if err != nil {
		slog.Warn("failed to load the queue to report a capacity wait", "chat_id", c.ID, "error", err)
		return
	}
	for i, g := range pending {
		if g.Status != GenerationStatusQueued {
			continue
		}
		emitter := newEventEmitter(ctx, s.repo, s.bus, g.ID, c.ID)
		emitter.emit(ctx, EventTypeQueued, map[string]any{"position": i, "prompt_preview": PromptPreview(g.Prompt)})
		return
	}
}

package themebuild

import (
	"context"
	"sync"
	"time"

	"ai-chat/internal/modules/chat"
)

// runTracker counts running generation loops so shutdown can wait for them. The draining flag and the count share
// one lock: a loop either starts before draining begins (and is waited for) or is refused.
type runTracker struct {
	mu       sync.Mutex
	draining bool
	running  int
	// The generation each loop is running now, for re-queuing at the drain limit; bounded by concurrent loops.
	generations map[string]chat.Chat
}

func (t *runTracker) track(genID string, c chat.Chat) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.generations == nil {
		t.generations = make(map[string]chat.Chat)
	}
	t.generations[genID] = c
}

func (t *runTracker) untrack(genID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.generations, genID)
}

func (t *runTracker) runningGenerations() map[string]chat.Chat {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]chat.Chat, len(t.generations))
	for id, c := range t.generations {
		out[id] = c
	}
	return out
}

// start registers a loop about to run; false once draining has begun, and the caller must leave the work queued.
func (t *runTracker) start() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.draining {
		return false
	}
	t.running++
	return true
}

func (t *runTracker) done() {
	t.mu.Lock()
	t.running--
	t.mu.Unlock()
}

func (t *runTracker) isDraining() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.draining
}

func (t *runTracker) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.running
}

// drainPollInterval bounds how late Drain notices the last loop finishing.
const drainPollInterval = 100 * time.Millisecond

// Drain stops new generations from starting and waits for running ones, up to limit. A generation that finishes
// leaves the rest of its chat's queue queued. finished counts loops that ended within the limit; stillRunning is what
// the process exit will cut off.
func (s *Service) Drain(ctx context.Context, limit time.Duration) (finished, stillRunning int) {
	s.runs.mu.Lock()
	s.runs.draining = true
	started := s.runs.running
	s.runs.mu.Unlock()

	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(drainPollInterval)
	defer tick.Stop()
	for {
		remaining := s.runs.count()
		if remaining == 0 {
			return started, 0
		}
		select {
		case <-ctx.Done():
			return started - remaining, remaining
		case <-deadline.C:
			return started - remaining, remaining
		case <-tick.C:
		}
	}
}

package themebuild

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestRunOneQueuedGeneration_TickerUpdatesHeartbeatWithNoEvents is Bug 2's
// layer-2 test: eventEmitter.emitLive (layer 1) only fires on a thinking
// delta, but ToolChoiceAny is forced on every tool-loop iteration (see
// ai.Generate), so a turn that goes straight to a tool call — or, as here,
// a generator that emits nothing at all — produces no delta and no durable
// event for the whole length of one model call. Before this fix, a call
// that ran longer than generationHeartbeatTimeout with zero events would
// leave last_heartbeat_at stale despite the generation being perfectly
// healthy, and ReapStaleGenerations would wrongly reap it. This drives
// runOneQueuedGeneration with a generator that ignores onDelta/progress
// entirely and just sleeps, and asserts the ticker alone still keeps the
// heartbeat fresh.
func TestRunOneQueuedGeneration_TickerUpdatesHeartbeatWithNoEvents(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)

	// Real-world heartbeatThrottle/ticker interval is 30s — shrunk here so
	// the test doesn't have to wait that long for a tick. Restored via
	// t.Cleanup so other tests in this package keep seeing the real value.
	originalTicker := heartbeatTickerNanos.Load()
	heartbeatTickerNanos.Store(int64(30 * time.Millisecond))
	t.Cleanup(func() { heartbeatTickerNanos.Store(originalTicker) })

	gen := &scriptedGenerator{results: []scriptedResult{{delay: 200 * time.Millisecond}}}
	svc.gen = gen

	ctx := context.Background()
	tenantID := uint64(time.Now().UnixNano())
	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}

	genID := uuid.NewString()
	if err := svc.repo.StartGeneration(ctx, genID, c.ID, tenantID); err != nil {
		t.Fatalf("StartGeneration failed: %v", err)
	}
	svc.tokens.store(genID, "tok")

	g := Generation{ID: genID, ChatID: c.ID, TenantID: tenantID, ThemeSlug: "test-theme", Prompt: "do something"}

	before := time.Now()
	svc.runOneQueuedGeneration(ctx, c, g)

	hb := getHeartbeat(t, svc.repo.db, genID)
	if !hb.Valid {
		t.Fatal("expected the ticker to have stamped last_heartbeat_at despite the generator emitting no events")
	}
	if hb.Time.Before(before) {
		t.Errorf("expected last_heartbeat_at to have been updated during this call, got %v (before test started: %v)", hb.Time, before)
	}

	// The ticker must stop once the generation ends — not keep firing
	// against a `generations` row for a generation that's already done.
	// Confirmed by checking last_heartbeat_at doesn't keep advancing after
	// runOneQueuedGeneration has already returned.
	stopped := getHeartbeat(t, svc.repo.db, genID)
	time.Sleep(150 * time.Millisecond) // several ticker intervals
	stillStopped := getHeartbeat(t, svc.repo.db, genID)
	if !stillStopped.Time.Equal(stopped.Time) {
		t.Errorf("expected the ticker to have stopped once the generation ended, but the heartbeat kept advancing: %v -> %v", stopped.Time, stillStopped.Time)
	}
}

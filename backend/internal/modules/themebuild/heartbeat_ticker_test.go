package themebuild

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Bug 2: ticker must keep heartbeat fresh even with zero events from the model.
func TestRunOneQueuedGeneration_TickerUpdatesHeartbeatWithNoEvents(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)

	// Shrink heartbeat ticker interval for faster testing.
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

	// Ticker must stop once generation ends; verify heartbeat stops advancing.
	stopped := getHeartbeat(t, svc.repo.db, genID)
	time.Sleep(150 * time.Millisecond) // several ticker intervals
	stillStopped := getHeartbeat(t, svc.repo.db, genID)
	if !stillStopped.Time.Equal(stopped.Time) {
		t.Errorf("expected the ticker to have stopped once the generation ended, but the heartbeat kept advancing: %v -> %v", stopped.Time, stillStopped.Time)
	}
}

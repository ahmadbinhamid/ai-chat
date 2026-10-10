package themebuild

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"

	"github.com/google/uuid"
)

// gatedGenerator holds every call until the test sends on (or closes) gate, recording how many ran at once.
type gatedGenerator struct {
	gate chan struct{}

	mu       sync.Mutex
	inFlight int
	peak     int
}

func (g *gatedGenerator) Generate(ctx context.Context, _ ai.ThemeContext, _ []ai.Turn, prompt string, _ []ai.Image, _ ai.ToolProgress, _ ai.ToolExecutor, _ ai.FileReader) (*ai.Result, error) {
	g.mu.Lock()
	g.inFlight++
	g.peak = max(g.peak, g.inFlight)
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.inFlight--
		g.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-g.gate:
	}
	return &ai.Result{Summary: "[gated] " + prompt, AnsweredQuestion: true}, nil
}

func (g *gatedGenerator) counts() (inFlight, peak int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inFlight, g.peak
}

func (*gatedGenerator) Summarize(context.Context, string, []ai.Turn) (string, error) { return "", nil }

func (*gatedGenerator) SupportsVision() bool { return false }

// startQueuedOnNewChat gives tenantID another chat (chat type kind) with one queued prompt and starts its queue.
func startQueuedOnNewChat(t *testing.T, svc *Service, chatSvc *chat.Service, tenantID uint64, kind string) (chat.Chat, string) {
	t.Helper()
	ctx := context.Background()
	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, kind)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}
	genID := uuid.NewString()
	if _, err := svc.repo.EnqueueGeneration(ctx, Generation{ID: genID, ChatID: c.ID, TenantID: tenantID, Prompt: kind, ThemeSlug: "theme"}); err != nil {
		t.Fatalf("EnqueueGeneration failed: %v", err)
	}
	svc.tokens.store(genID, "t")
	if _, err := svc.startChatQueue(ctx, c); err != nil && !errors.Is(err, errAwaitingCapacity) {
		t.Fatalf("startChatQueue failed: %v", err)
	}
	return c, genID
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func generationStatus(t *testing.T, svc *Service, chatID, genID string) string {
	t.Helper()
	g, err := svc.repo.GetGenerationByID(context.Background(), chatID, genID)
	if err != nil {
		t.Fatalf("GetGenerationByID failed: %v", err)
	}
	return g.Status
}

func TestCapacity_ThirdGenerationForTenantStaysQueuedWhileTwoRun(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &gatedGenerator{gate: make(chan struct{})}
	svc.gen = gen
	svc.SetGenerationLimits(0, 2)
	tenantID := uint64(time.Now().UnixNano())

	c1, g1 := startQueuedOnNewChat(t, svc, chatSvc, tenantID, "cap-a")
	c2, g2 := startQueuedOnNewChat(t, svc, chatSvc, tenantID, "cap-b")
	waitFor(t, "two generations in flight", func() bool { n, _ := gen.counts(); return n == 2 })
	c3, g3 := startQueuedOnNewChat(t, svc, chatSvc, tenantID, "cap-c")

	time.Sleep(300 * time.Millisecond)
	if _, peak := gen.counts(); peak != 2 {
		t.Fatalf("peak concurrent generations = %d, want 2", peak)
	}
	if got := generationStatus(t, svc, c3.ID, g3); got != GenerationStatusQueued {
		t.Fatalf("third generation status = %q while two run, want %q", got, GenerationStatusQueued)
	}

	var heartbeat sql.NullTime
	if err := svc.repo.db.QueryRowContext(context.Background(), `SELECT last_heartbeat_at FROM generations WHERE id = ?`, g3).Scan(&heartbeat); err != nil {
		t.Fatalf("read heartbeat: %v", err)
	}
	if !heartbeat.Valid {
		t.Fatal("expected the waiting chat's queued row to be heartbeated")
	}
	orphaned, err := svc.repo.ChatsWithOrphanedQueues(context.Background(), time.Now().UTC().Add(-generationHeartbeatTimeout))
	if err != nil {
		t.Fatalf("ChatsWithOrphanedQueues failed: %v", err)
	}
	if slices.Contains(orphaned, c3.ID) {
		t.Fatal("a chat waiting for capacity must not be reported as an orphaned queue")
	}

	close(gen.gate)
	for _, p := range []struct{ chatID, genID string }{{c1.ID, g1}, {c2.ID, g2}, {c3.ID, g3}} {
		waitFor(t, "generation "+p.genID+" to succeed", func() bool {
			return generationStatus(t, svc, p.chatID, p.genID) == GenerationStatusSucceeded
		})
	}
	if _, peak := gen.counts(); peak != 2 {
		t.Fatalf("peak concurrent generations = %d after the queue drained, want 2", peak)
	}
	waitFor(t, "every loop to finish", func() bool { return svc.runs.count() == 0 })
	if total, tenant := svc.limiter.Running(tenantID); total != 0 || tenant != 0 {
		t.Fatalf("limiter still holds %d slots (%d for the tenant) after everything finished", total, tenant)
	}
}

func TestCapacity_CancelWhileWaitingEndsTheWait(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &gatedGenerator{gate: make(chan struct{})}
	svc.gen = gen
	svc.SetGenerationLimits(1, 0)
	tenantID := uint64(time.Now().UnixNano())

	c1, g1 := startQueuedOnNewChat(t, svc, chatSvc, tenantID, "cap-a")
	waitFor(t, "first generation in flight", func() bool { n, _ := gen.counts(); return n == 1 })
	c2, g2 := startQueuedOnNewChat(t, svc, chatSvc, tenantID, "cap-b")
	waitFor(t, "second loop waiting", func() bool { return svc.runs.count() == 2 })

	if err := svc.CancelQueuedGeneration(context.Background(), tenantID, c2.ID, g2); err != nil {
		t.Fatalf("CancelQueuedGeneration failed: %v", err)
	}
	waitFor(t, "the waiting loop to stop", func() bool { return svc.runs.count() == 1 })

	close(gen.gate)
	waitFor(t, "first generation to succeed", func() bool {
		return generationStatus(t, svc, c1.ID, g1) == GenerationStatusSucceeded
	})
	if got := generationStatus(t, svc, c2.ID, g2); got != GenerationStatusCancelled {
		t.Fatalf("cancelled generation status = %q, want %q", got, GenerationStatusCancelled)
	}
	waitFor(t, "every loop to finish", func() bool { return svc.runs.count() == 0 })
}

func TestCapacity_DrainEndsTheWait(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &gatedGenerator{gate: make(chan struct{})}
	svc.gen = gen
	svc.SetGenerationLimits(1, 0)
	tenantID := uint64(time.Now().UnixNano())

	c1, g1 := startQueuedOnNewChat(t, svc, chatSvc, tenantID, "cap-a")
	waitFor(t, "first generation in flight", func() bool { n, _ := gen.counts(); return n == 1 })
	c2, g2 := startQueuedOnNewChat(t, svc, chatSvc, tenantID, "cap-b")
	waitFor(t, "second loop waiting", func() bool { return svc.runs.count() == 2 })

	go func() {
		time.Sleep(100 * time.Millisecond)
		close(gen.gate)
	}()
	if _, stillRunning := svc.Drain(context.Background(), 5*time.Second); stillRunning != 0 {
		t.Fatalf("Drain left %d loops running; a capacity wait must not hold it up", stillRunning)
	}
	if got := generationStatus(t, svc, c1.ID, g1); got != GenerationStatusSucceeded {
		t.Fatalf("running generation status = %q, want %q", got, GenerationStatusSucceeded)
	}
	if got := generationStatus(t, svc, c2.ID, g2); got != GenerationStatusQueued {
		t.Fatalf("waiting generation status = %q after drain, want it left %q for the next process", got, GenerationStatusQueued)
	}
}

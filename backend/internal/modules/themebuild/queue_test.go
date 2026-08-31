package themebuild

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// seedQueued inserts a queued row directly for chatID via EnqueueGeneration,
// returning its ID and the position EnqueueGeneration reported.
func seedQueued(t *testing.T, repo *Repository, chatID string, prompt string) (id string, position int) {
	t.Helper()
	id = uuid.NewString()
	position, err := repo.EnqueueGeneration(context.Background(), Generation{
		ID: id, ChatID: chatID, TenantID: 1, Prompt: prompt, ThemeSlug: "test-theme",
	})
	if err != nil {
		t.Fatalf("EnqueueGeneration failed: %v", err)
	}
	return id, position
}

func TestRepository_DequeueNext_OrdersByQueuedAt(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()

	first, pos0 := seedQueued(t, repo, chatID, "first")
	if pos0 != 0 {
		t.Fatalf("expected the first enqueue to report position 0, got %d", pos0)
	}
	// queued_at has only second-level precision (see DequeueNext's doc
	// comment on the id tie-break) — without this gap the two rows could
	// tie on queued_at and this test would be asserting an arbitrary,
	// non-chronological id-ordered outcome instead of the ordering it
	// actually means to check.
	time.Sleep(1100 * time.Millisecond)
	second, pos1 := seedQueued(t, repo, chatID, "second")
	if pos1 != 1 {
		t.Fatalf("expected the second enqueue to report position 1 (one ahead of it), got %d", pos1)
	}

	got, err := repo.DequeueNext(ctx, chatID)
	if err != nil {
		t.Fatalf("DequeueNext failed: %v", err)
	}
	if got.ID != first {
		t.Fatalf("expected the oldest queued row (%s) to dequeue first, got %s", first, got.ID)
	}
	if got.Status != GenerationStatusRunning || got.StartedAt == nil {
		t.Fatalf("expected the dequeued row to be promoted to running with started_at set, got %+v", got)
	}
	if got.Prompt != "first" || got.ThemeSlug != "test-theme" {
		t.Fatalf("expected the dequeued row to carry its enqueued prompt/theme_slug, got %+v", got)
	}

	// End the first before dequeuing again — DequeueNext is blocked by a
	// running row (see TestRepository_DequeueNext_WhileRunning below).
	if err := repo.EndGeneration(ctx, chatID, nil); err != nil {
		t.Fatalf("EndGeneration failed: %v", err)
	}

	got2, err := repo.DequeueNext(ctx, chatID)
	if err != nil {
		t.Fatalf("second DequeueNext failed: %v", err)
	}
	if got2.ID != second {
		t.Fatalf("expected the second-oldest row (%s) to dequeue next, got %s", second, got2.ID)
	}
}

func TestRepository_DequeueNext_EmptyQueueReturnsErrNotFound(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	chatID := uuid.NewString()

	if _, err := repo.DequeueNext(context.Background(), chatID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for an empty queue, got %v", err)
	}
}

func TestRepository_DequeueNext_WhileRunningReturnsErrGenerationInProgress(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()

	if err := repo.StartGeneration(ctx, uuid.NewString(), chatID, 1); err != nil {
		t.Fatalf("StartGeneration failed: %v", err)
	}
	seedQueued(t, repo, chatID, "waiting")

	if _, err := repo.DequeueNext(ctx, chatID); !errors.Is(err, ErrGenerationInProgress) {
		t.Fatalf("expected ErrGenerationInProgress while something is running, got %v", err)
	}
}

// TestRepository_DequeueNext_ConcurrentRace fires two DequeueNext calls at
// the same chat at once — exactly one must win the running slot, the other
// must lose with ErrGenerationInProgress, never both succeeding (which
// would mean two callers both think they're running a generation for the
// same chat at once — exactly what uniq_generations_running_chat exists to
// make impossible). Seeds two queued rows, not one: with only one queued
// row, the loser's UPDATE would just match zero rows (ErrNotFound) once the
// winner claims it, which proves nothing about the unique index; with two,
// the loser's UPDATE still finds a row to attempt promoting and collides
// with the winner's on write, which is the actual race this index guards
// against. Run with -race.
func TestRepository_DequeueNext_ConcurrentRace(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()
	seedQueued(t, repo, chatID, "one")
	seedQueued(t, repo, chatID, "two")

	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := repo.DequeueNext(ctx, chatID)
			results[i] = err
		}(i)
	}
	wg.Wait()

	successes, conflicts := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrGenerationInProgress):
			conflicts++
		default:
			t.Fatalf("unexpected error from concurrent DequeueNext: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("expected exactly one winner and one ErrGenerationInProgress, got %d successes / %d conflicts", successes, conflicts)
	}
}

func TestRepository_CancelQueued(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()

	queuedID, _ := seedQueued(t, repo, chatID, "cancel me")
	if err := repo.CancelQueued(ctx, chatID, queuedID); err != nil {
		t.Fatalf("expected cancelling a queued row to succeed, got %v", err)
	}

	g, err := repo.GetGeneration(ctx, chatID)
	if err != nil {
		t.Fatalf("GetGeneration failed: %v", err)
	}
	if g.Status != GenerationStatusCancelled {
		t.Fatalf("expected status %q after cancel, got %q", GenerationStatusCancelled, g.Status)
	}

	// A cancelled row is a dead end: dequeuing again must skip it, not
	// resurrect it.
	if _, err := repo.DequeueNext(ctx, chatID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected the queue to be empty after cancelling its only row, got %v", err)
	}
}

func TestRepository_CancelQueued_RunningRowIsRejected(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()

	runningID := uuid.NewString()
	if err := repo.StartGeneration(ctx, runningID, chatID, 1); err != nil {
		t.Fatalf("StartGeneration failed: %v", err)
	}

	if err := repo.CancelQueued(ctx, chatID, runningID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected CancelQueued to reject a running row (ErrNotFound, never touching it), got %v", err)
	}

	g, err := repo.GetGeneration(ctx, chatID)
	if err != nil {
		t.Fatalf("GetGeneration failed: %v", err)
	}
	if g.Status != GenerationStatusRunning {
		t.Fatalf("expected the running row to be untouched, got status %q", g.Status)
	}
}

func TestRepository_GetGenerationByID(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()

	queuedID, _ := seedQueued(t, repo, chatID, "find me")

	g, err := repo.GetGenerationByID(ctx, chatID, queuedID)
	if err != nil {
		t.Fatalf("GetGenerationByID failed: %v", err)
	}
	if g.ID != queuedID || g.Status != GenerationStatusQueued {
		t.Fatalf("expected the queued row back, got %+v", g)
	}

	if _, err := repo.GetGenerationByID(ctx, chatID, uuid.NewString()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for an unknown id, got %v", err)
	}
	if _, err := repo.GetGenerationByID(ctx, uuid.NewString(), queuedID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for the right id under the wrong chat, got %v", err)
	}
}

func TestRepository_EndGenerationCancelled(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()

	runningID := uuid.NewString()
	if err := repo.StartGeneration(ctx, runningID, chatID, 1); err != nil {
		t.Fatalf("StartGeneration failed: %v", err)
	}

	if err := repo.EndGenerationCancelled(ctx, chatID); err != nil {
		t.Fatalf("EndGenerationCancelled failed: %v", err)
	}

	g, err := repo.GetGeneration(ctx, chatID)
	if err != nil {
		t.Fatalf("GetGeneration failed: %v", err)
	}
	if g.Status != GenerationStatusCancelled {
		t.Fatalf("expected status %q, got %q", GenerationStatusCancelled, g.Status)
	}
	if g.Error != nil {
		t.Fatalf("expected no error recorded for a cancelled generation, got %q", *g.Error)
	}
}

func TestRepository_EnqueueGeneration_RejectsPastQueueCap(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()

	for i := 0; i < maxQueueDepth; i++ {
		if _, err := repo.EnqueueGeneration(ctx, Generation{
			ID: uuid.NewString(), ChatID: chatID, TenantID: 1, Prompt: "p", ThemeSlug: "t",
		}); err != nil {
			t.Fatalf("enqueue %d/%d failed: %v", i+1, maxQueueDepth, err)
		}
	}

	if _, err := repo.EnqueueGeneration(ctx, Generation{
		ID: uuid.NewString(), ChatID: chatID, TenantID: 1, Prompt: "one too many", ThemeSlug: "t",
	}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected ErrQueueFull past the cap, got %v", err)
	}
}

func TestRepository_ListPending_RunningFirstThenQueuedOldestFirst(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()

	runningID := uuid.NewString()
	if err := repo.StartGeneration(ctx, runningID, chatID, 1); err != nil {
		t.Fatalf("StartGeneration failed: %v", err)
	}
	q1, _ := seedQueued(t, repo, chatID, "one")
	time.Sleep(1100 * time.Millisecond) // queued_at has only second-level precision
	q2, _ := seedQueued(t, repo, chatID, "two")

	pending, err := repo.ListPending(ctx, chatID)
	if err != nil {
		t.Fatalf("ListPending failed: %v", err)
	}
	if len(pending) != 3 {
		t.Fatalf("expected 3 pending rows (1 running + 2 queued), got %d: %+v", len(pending), pending)
	}
	if pending[0].ID != runningID || pending[0].Status != GenerationStatusRunning {
		t.Fatalf("expected the running row first, got %+v", pending[0])
	}
	if pending[1].ID != q1 || pending[2].ID != q2 {
		t.Fatalf("expected queued rows oldest first (%s, %s), got (%s, %s)", q1, q2, pending[1].ID, pending[2].ID)
	}
}

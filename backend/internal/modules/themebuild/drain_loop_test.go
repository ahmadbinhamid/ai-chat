package themebuild

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/themefs"

	"github.com/google/uuid"
)

// Returns one canned result per call in order; lets tests control success/fail/timing per generation.
type scriptedGenerator struct {
	mu      sync.Mutex
	calls   int
	results []scriptedResult
}

type scriptedResult struct {
	delay time.Duration
	err   error
}

func (g *scriptedGenerator) Generate(ctx context.Context, _ ai.ThemeContext, _ []ai.Turn, prompt string, _ []ai.Image, _ func(string), _ ai.ToolProgress, _ ai.ToolExecutor, _ ai.FileReader) (*ai.Result, error) {
	g.mu.Lock()
	i := g.calls
	g.calls++
	g.mu.Unlock()

	r := scriptedResult{}
	switch {
	case i < len(g.results):
		r = g.results[i]
	case len(g.results) > 0:
		r = g.results[len(g.results)-1]
	}

	if r.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(r.delay):
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	return &ai.Result{Summary: "[scripted] " + prompt}, nil
}

func (g *scriptedGenerator) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

func (*scriptedGenerator) Summarize(context.Context, []ai.Turn) (string, error) { return "", nil }

func (*scriptedGenerator) SupportsVision() bool { return false }

// Real test DB; store returns empty tree for ListFiles, 404 for reads (sufficient for end-to-end tests).
func newQueueTestService(t *testing.T) (*Service, *chat.Service) {
	t.Helper()
	conn := openTestDB(t)
	chatSvc := chat.NewService(chat.NewRepository(conn))
	buildRepo := NewRepository(conn)

	storeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/store/themes/active/files" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"files":[]}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(storeServer.Close)

	svc := NewService(buildRepo, chatSvc, nil, themefs.NewStore(storeServer.URL), nil)
	return svc, chatSvc
}

func waitForCalls(t *testing.T, gen *scriptedGenerator, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if gen.callCount() >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d generator calls, got %d", n, gen.callCount())
}

// Item 1: two prompts same chat; first runs immediately, second queues behind it.
func TestGenerate_SecondPromptQueuesWhileFirstRuns(t *testing.T) {
	svc, _ := newQueueTestService(t)
	gen := &scriptedGenerator{results: []scriptedResult{{delay: 300 * time.Millisecond}, {}}}
	svc.gen = gen

	tenantID := uint64(time.Now().UnixNano())
	ctx := context.Background()

	out1, err := svc.Generate(ctx, GenerateInput{TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme", Prompt: "first"})
	if err != nil {
		t.Fatalf("first Generate failed: %v", err)
	}
	if out1.QueuePosition != 0 {
		t.Fatalf("expected the first prompt to run immediately (position 0), got %d", out1.QueuePosition)
	}

	out2, err := svc.Generate(ctx, GenerateInput{TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme", Prompt: "second"})
	if err != nil {
		t.Fatalf("second Generate failed: %v", err)
	}
	if out2.QueuePosition != 1 {
		t.Fatalf("expected the second prompt to queue behind the first (position 1), got %d", out2.QueuePosition)
	}
	if out2.Chat.ID != out1.Chat.ID {
		t.Fatalf("expected both prompts on the same chat, got %s and %s", out1.Chat.ID, out2.Chat.ID)
	}

	waitForCalls(t, gen, 2, 5*time.Second)
}

// Item 6: three prompts all run in order; proves drain loop dequeues entire queue.
// Known flaky (queue-ordering flake hides ordering bugs; investigate before trusting).
func TestRunGeneration_DrainsWholeQueueInOrder(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &scriptedGenerator{results: []scriptedResult{
		{delay: 150 * time.Millisecond},
		{delay: 50 * time.Millisecond},
		{delay: 50 * time.Millisecond},
	}}
	svc.gen = gen

	tenantID := uint64(time.Now().UnixNano())
	ctx := context.Background()
	prompts := []string{"first", "second", "third"}
	var chatID string
	for _, p := range prompts {
		out, err := svc.Generate(ctx, GenerateInput{TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme", Prompt: p})
		if err != nil {
			t.Fatalf("Generate(%q) failed: %v", p, err)
		}
		chatID = out.Chat.ID
	}

	waitForCalls(t, gen, 3, 10*time.Second)
	// Generator called 3 times doesn't guarantee third call's EndGeneration landed; give it time.
	time.Sleep(200 * time.Millisecond)

	messages, err := chatSvc.ListMessagesForVerifiedChat(ctx, chatID)
	if err != nil {
		t.Fatalf("ListMessagesForVerifiedChat failed: %v", err)
	}
	var assistantMsgs []chat.Message
	for _, m := range messages {
		if m.Role == chat.RoleAssistant {
			assistantMsgs = append(assistantMsgs, m)
		}
	}
	if len(assistantMsgs) != 3 {
		t.Fatalf("expected 3 assistant replies (one per queued prompt), got %d: %+v", len(assistantMsgs), assistantMsgs)
	}
	for i, m := range assistantMsgs {
		if m.Status != chat.MessageStatusCompleted {
			t.Errorf("assistant message %d: expected completed, got %q", i, m.Status)
		}
	}
}

// Item 7: failure in generation 1 doesn't stop 2 and 3; failure isolation not queue-wide cancellation.
func TestRunGeneration_FailureDoesNotStopLaterQueuedPrompts(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	boom := context.DeadlineExceeded // any non-nil error the scripted generator can return
	gen := &scriptedGenerator{results: []scriptedResult{
		{err: boom},
		{},
		{},
	}}
	svc.gen = gen

	tenantID := uint64(time.Now().UnixNano())
	ctx := context.Background()
	var chatID string
	for _, p := range []string{"fails", "second", "third"} {
		out, err := svc.Generate(ctx, GenerateInput{TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme", Prompt: p})
		if err != nil {
			t.Fatalf("Generate(%q) failed: %v", p, err)
		}
		chatID = out.Chat.ID
	}

	waitForCalls(t, gen, 3, 10*time.Second)
	time.Sleep(200 * time.Millisecond)

	messages, err := chatSvc.ListMessagesForVerifiedChat(ctx, chatID)
	if err != nil {
		t.Fatalf("ListMessagesForVerifiedChat failed: %v", err)
	}
	var failed, completed int
	for _, m := range messages {
		if m.Role != chat.RoleAssistant {
			continue
		}
		switch m.Status {
		case chat.MessageStatusFailed:
			failed++
		case chat.MessageStatusCompleted:
			completed++
		}
	}
	if failed != 1 {
		t.Errorf("expected exactly 1 failed assistant message, got %d", failed)
	}
	if completed != 2 {
		t.Errorf("expected the other 2 queued prompts to still complete, got %d", completed)
	}
}

// Item 11: each iteration gets fresh generateTimeout budget; combined time can exceed per-iteration cap.
// Known flaky (queue-ordering flake hides ordering bugs; investigate before trusting).
func TestRunGeneration_EachIterationGetsFreshTimeout(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &scriptedGenerator{results: []scriptedResult{
		{delay: 120 * time.Millisecond},
		{delay: 120 * time.Millisecond},
		{delay: 120 * time.Millisecond},
	}}
	svc.gen = gen

	original := generateTimeoutNanos.Load()
	generateTimeoutNanos.Store(int64(200 * time.Millisecond)) // less than the 3 calls' combined ~360ms
	t.Cleanup(func() { generateTimeoutNanos.Store(original) })

	tenantID := uint64(time.Now().UnixNano())
	ctx := context.Background()
	var chatID string
	for _, p := range []string{"a", "b", "c"} {
		out, err := svc.Generate(ctx, GenerateInput{TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme", Prompt: p})
		if err != nil {
			t.Fatalf("Generate(%q) failed: %v", p, err)
		}
		chatID = out.Chat.ID
	}

	waitForCalls(t, gen, 3, 10*time.Second)
	time.Sleep(200 * time.Millisecond)

	messages, err := chatSvc.ListMessagesForVerifiedChat(ctx, chatID)
	if err != nil {
		t.Fatalf("ListMessagesForVerifiedChat failed: %v", err)
	}
	var completed int
	for _, m := range messages {
		if m.Role == chat.RoleAssistant && m.Status == chat.MessageStatusCompleted {
			completed++
		}
	}
	if completed != 3 {
		t.Fatalf("expected all 3 generations to complete under their own fresh timeouts, got %d completed out of 3", completed)
	}
}

// Cancel RUNNING generation (not queued) must interrupt it via EventTypeCancelRequested (2s delay vs <2s poll).
func TestGenerate_CancelWhileRunning_StopsGenerationWithNoAssistantMessage(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &scriptedGenerator{results: []scriptedResult{{delay: 2 * time.Second}}}
	svc.gen = gen

	tenantID := uint64(time.Now().UnixNano())
	ctx := context.Background()

	out, err := svc.Generate(ctx, GenerateInput{TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme", Prompt: "cancel me"})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	waitForCalls(t, gen, 1, 5*time.Second) // Row genuinely running before cancel.

	if err := svc.CancelQueuedGeneration(ctx, tenantID, out.Chat.ID, out.GenerationID); err != nil {
		t.Fatalf("CancelQueuedGeneration on a running row failed: %v", err)
	}

	deadline := time.Now().Add(1500 * time.Millisecond) // well short of the 2s scripted delay
	var g Generation
	for time.Now().Before(deadline) {
		g, err = svc.repo.GetGeneration(ctx, out.Chat.ID)
		if err != nil {
			t.Fatalf("GetGeneration failed: %v", err)
		}
		if g.Status == GenerationStatusCancelled {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if g.Status != GenerationStatusCancelled {
		t.Fatalf("expected the running generation to be cancelled well before its 2s scripted delay elapsed, last observed status %q", g.Status)
	}
	if g.Error != nil {
		t.Fatalf("expected no error recorded for a cancelled generation, got %q", *g.Error)
	}

	messages, err := chatSvc.ListMessagesForVerifiedChat(ctx, out.Chat.ID)
	if err != nil {
		t.Fatalf("ListMessagesForVerifiedChat failed: %v", err)
	}
	for _, m := range messages {
		if m.Role == chat.RoleAssistant {
			t.Fatalf("expected no assistant message for a cancelled turn, got one: %+v", m)
		}
	}
}

// Cancel request recorded before Subscribe (gap between DequeueNext and Subscribe); post-subscribe check catches it.
func TestRunOneQueuedGeneration_HonorsCancelRequestedBeforeSubscribing(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &scriptedGenerator{results: []scriptedResult{{delay: 2 * time.Second}}}
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
	if err := svc.repo.RequestGenerationCancellation(ctx, c.ID, genID); err != nil {
		t.Fatalf("RequestGenerationCancellation failed: %v", err)
	}

	g := Generation{ID: genID, ChatID: c.ID, TenantID: tenantID, ThemeSlug: "test-theme", Prompt: "do something"}

	start := time.Now()
	svc.runOneQueuedGeneration(ctx, c, g)
	elapsed := time.Since(start)

	if elapsed > 1*time.Second {
		t.Fatalf("expected the pre-existing cancel request to be caught immediately after subscribing, took %v (scripted delay was 2s)", elapsed)
	}

	got, err := svc.repo.GetGeneration(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetGeneration failed: %v", err)
	}
	if got.Status != GenerationStatusCancelled {
		t.Fatalf("expected status %q, got %q", GenerationStatusCancelled, got.Status)
	}
}

// Late cancel (after generation finishes) must never corrupt outcome; guards against relabeling success.
func TestRunOneQueuedGeneration_CancelAfterSuccessDoesNotRelabelOutcome(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &scriptedGenerator{results: []scriptedResult{{}}} // completes immediately, no delay
	svc.gen = gen

	tenantID := uint64(time.Now().UnixNano())
	ctx := context.Background()

	out, err := svc.Generate(ctx, GenerateInput{TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme", Prompt: "finish fast"})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	waitForCalls(t, gen, 1, 5*time.Second)
	// Race cancel against already-finishing generation (doGenerate likely already committed).
	_ = svc.CancelQueuedGeneration(ctx, tenantID, out.Chat.ID, out.GenerationID)

	deadline := time.Now().Add(2 * time.Second)
	var g Generation
	for time.Now().Before(deadline) {
		g, err = svc.repo.GetGeneration(ctx, out.Chat.ID)
		if err != nil {
			t.Fatalf("GetGeneration failed: %v", err)
		}
		if g.Status != GenerationStatusRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if g.Status != GenerationStatusSucceeded {
		t.Fatalf("expected a late cancel request to leave a genuinely successful generation as %q, got %q", GenerationStatusSucceeded, g.Status)
	}

	messages, err := chatSvc.ListMessagesForVerifiedChat(ctx, out.Chat.ID)
	if err != nil {
		t.Fatalf("ListMessagesForVerifiedChat failed: %v", err)
	}
	var completed int
	for _, m := range messages {
		if m.Role == chat.RoleAssistant && m.Status == chat.MessageStatusCompleted {
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("expected the completed assistant message to still be recorded despite the late cancel request, got %d completed messages", completed)
	}
}

// CancelAllPending stops running AND all queued prompts, else next queued starts (wrong UX for stop).
func TestCancelAllPending_StopsRunningAndEveryQueuedPrompt(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &scriptedGenerator{results: []scriptedResult{{delay: 2 * time.Second}}}
	svc.gen = gen

	tenantID := uint64(time.Now().UnixNano())
	ctx := context.Background()

	out, err := svc.Generate(ctx, GenerateInput{TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme", Prompt: "running"})
	if err != nil {
		t.Fatalf("Generate(running) failed: %v", err)
	}
	chatID := out.Chat.ID

	var queuedIDs []string
	for _, p := range []string{"queued one", "queued two"} {
		qOut, err := svc.Generate(ctx, GenerateInput{TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme", Prompt: p})
		if err != nil {
			t.Fatalf("Generate(%q) failed: %v", p, err)
		}
		queuedIDs = append(queuedIDs, qOut.GenerationID)
	}

	waitForCalls(t, gen, 1, 5*time.Second) // Running one genuinely running before cancel.

	if err := svc.CancelAllPending(ctx, tenantID, chatID); err != nil {
		t.Fatalf("CancelAllPending failed: %v", err)
	}

	// Queued rows cancelled synchronously.
	for _, id := range queuedIDs {
		g, err := svc.repo.GetGenerationByID(ctx, chatID, id)
		if err != nil {
			t.Fatalf("GetGenerationByID(%s) failed: %v", id, err)
		}
		if g.Status != GenerationStatusCancelled {
			t.Fatalf("expected queued generation %s to be cancelled, got %q", id, g.Status)
		}
	}

	// Running row stops asynchronously; poll well short of 2s scripted delay.
	deadline := time.Now().Add(1500 * time.Millisecond)
	var running Generation
	for time.Now().Before(deadline) {
		running, err = svc.repo.GetGenerationByID(ctx, chatID, out.GenerationID)
		if err != nil {
			t.Fatalf("GetGenerationByID(running) failed: %v", err)
		}
		if running.Status == GenerationStatusCancelled {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if running.Status != GenerationStatusCancelled {
		t.Fatalf("expected the running generation to be cancelled well before its 2s scripted delay, last observed status %q", running.Status)
	}

	// Queue fully drained; any surviving row would start running (exact scenario this guards against).
	if _, err := svc.repo.DequeueNext(ctx, chatID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected the queue to be fully drained after cancel-all, got %v", err)
	}

	messages, err := chatSvc.ListMessagesForVerifiedChat(ctx, chatID)
	if err != nil {
		t.Fatalf("ListMessagesForVerifiedChat failed: %v", err)
	}
	for _, m := range messages {
		if m.Role == chat.RoleAssistant {
			t.Fatalf("expected no assistant message from any cancelled turn, got one: %+v", m)
		}
	}
}

// CancelAllPending must fully cancel queue even when running finishes naturally mid-cancel (multi-pass retry).
func TestCancelAllPending_StillFullyCancelsWhenRunningOneFinishesNaturallyMidCancel(t *testing.T) {
	svc, _ := newQueueTestService(t)
	// 30ms delay: genuinely running when cancel starts, but finishes naturally mid-cancel of queued rows.
	gen := &scriptedGenerator{results: []scriptedResult{{delay: 30 * time.Millisecond}, {}, {}}}
	svc.gen = gen

	tenantID := uint64(time.Now().UnixNano())
	ctx := context.Background()

	out, err := svc.Generate(ctx, GenerateInput{TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme", Prompt: "running"})
	if err != nil {
		t.Fatalf("Generate(running) failed: %v", err)
	}
	chatID := out.Chat.ID

	for _, p := range []string{"queued one", "queued two"} {
		if _, err := svc.Generate(ctx, GenerateInput{TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme", Prompt: p}); err != nil {
			t.Fatalf("Generate(%q) failed: %v", p, err)
		}
	}

	// No wait for running to start; deliberately race cancel against settling (tests multi-pass retry).
	if err := svc.CancelAllPending(ctx, tenantID, chatID); err != nil {
		t.Fatalf("CancelAllPending failed: %v", err)
	}

	// Nothing should be running or queued shortly after; poll briefly (drain loop bookkeeping is async).
	deadline := time.Now().Add(2 * time.Second)
	var pending []Generation
	for time.Now().Before(deadline) {
		pending, err = svc.repo.ListPending(ctx, chatID)
		if err != nil {
			t.Fatalf("ListPending failed: %v", err)
		}
		if len(pending) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected the whole queue to end up fully cancelled/finished, but %d row(s) are still pending: %+v", len(pending), pending)
}

// Item 10: queued rows with nothing running are failed with session-expired message by reaper.
func TestReapOrphanedQueues_FailsStrandedRowsWithSessionExpiredMessage(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	ctx := context.Background()
	tenantID := uint64(time.Now().UnixNano())

	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}
	// Two queued rows, nothing running (simulates pod death between EnqueueGeneration and DequeueNext).
	for _, p := range []string{"orphan one", "orphan two"} {
		if _, err := svc.repo.EnqueueGeneration(ctx, Generation{
			ID: uuid.NewString(), ChatID: c.ID, TenantID: tenantID, Prompt: p, ThemeSlug: "theme",
		}); err != nil {
			t.Fatalf("EnqueueGeneration failed: %v", err)
		}
	}

	svc.reapOrphanedQueues(ctx)

	messages, err := chatSvc.ListMessagesForVerifiedChat(ctx, c.ID)
	if err != nil {
		t.Fatalf("ListMessagesForVerifiedChat failed: %v", err)
	}
	var failedCount int
	for _, m := range messages {
		if m.Role == chat.RoleAssistant && m.Status == chat.MessageStatusFailed {
			failedCount++
			if m.Content != errSessionExpired.Error() {
				t.Errorf("expected the session-expired message, got %q", m.Content)
			}
		}
	}
	if failedCount != 2 {
		t.Fatalf("expected both orphaned rows to be failed, got %d failed messages", failedCount)
	}

	if _, err := svc.repo.DequeueNext(ctx, c.ID); err == nil {
		t.Fatal("expected the queue to be fully drained (both rows failed), not still dequeueable")
	}
}

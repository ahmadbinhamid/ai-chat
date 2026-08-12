package themebuild

import (
	"context"
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

// scriptedGenerator returns one canned result per call, in order — letting
// a test control exactly which of a queue's several generations
// succeeds/fails and how long each takes, which ai.NewFake's single fixed
// delay/no-op result can't express. Calls past the end of results reuse the
// last entry.
type scriptedGenerator struct {
	mu      sync.Mutex
	calls   int
	results []scriptedResult
}

type scriptedResult struct {
	delay time.Duration
	err   error
}

func (g *scriptedGenerator) Generate(ctx context.Context, _ ai.ThemeContext, _ []ai.Turn, prompt string, _ func(string), _ ai.ToolExecutor) (*ai.Result, error) {
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

// newQueueTestService builds a Service backed by the real test DB/chat
// service and a store standing in for flowpos-backend's theme-file API:
// ListFiles reports an empty tree (200), every individual file read 404s
// ("doesn't exist yet" — themefs.Store.ReadFile's normal, non-error case).
// That's enough for buildThemeContext/doGenerate to run end-to-end against
// a scriptedGenerator without ever touching a real theme or proposing any
// changes.
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

// waitForCalls polls until gen has made at least n calls or t seconds pass.
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

// Item 1: two Generate calls in a row for the same chat — the first runs,
// the second queues, neither errors.
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

// Item 6: three queued prompts from one Generate call each all run, in
// order — proving the drain loop actually dequeues the rest of the queue
// instead of stopping after the one it started with.
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
	// The generator having been called 3 times isn't itself proof the third
	// call's own bookkeeping (EndGeneration) landed yet — give it a beat.
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

// Item 7: a failure in generation 1 must not stop generations 2 and 3 —
// failure isolation, not queue-wide cancellation.
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

// Item 11: each drain-loop iteration gets its own generateTimeout budget —
// a queue of prompts that individually fit comfortably within a shrunk
// generateTimeout must all still succeed even though their *combined*
// runtime exceeds it, proving the budget resets per iteration instead of
// being computed once for the whole queue.
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

// Item 10: a chat with queued rows and nothing running gets those rows
// failed with the session-expired message by the reaper, rather than left
// stranded forever.
func TestReapOrphanedQueues_FailsStrandedRowsWithSessionExpiredMessage(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	ctx := context.Background()
	tenantID := uint64(time.Now().UnixNano())

	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}
	// Two queued rows, nothing running — simulates a pod that died between
	// EnqueueGeneration and DequeueNext ever happening for this chat, so
	// there's no in-memory token for either (this test never calls
	// s.tokens.store, matching that scenario exactly).
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

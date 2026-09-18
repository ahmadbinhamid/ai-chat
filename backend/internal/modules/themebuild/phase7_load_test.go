package themebuild

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"ai-chat/internal/modules/chat"
	"ai-chat/internal/prodhardening"
	"ai-chat/internal/themefs"

	"github.com/google/uuid"
)

func TestPhase7_PendingTokensSoftCap(t *testing.T) {
	p := newPendingTokens()
	capN := prodhardening.DefaultPolicy().PendingTokensSoftCap
	for i := 0; i < capN; i++ {
		p.store(fmt.Sprintf("g-%d", i), "tok")
	}
	if p.lenForTest() != capN {
		t.Fatalf("size=%d want %d", p.lenForTest(), capN)
	}
	p.store("g-overflow", "tok")
	if p.lenForTest() != capN {
		t.Fatalf("soft cap broken size=%d", p.lenForTest())
	}
	if !p.has("g-0") {
		t.Fatal("existing entry should remain")
	}
	if p.has("g-overflow") {
		t.Fatal("overflow store must be refused")
	}
}

func TestPhase7_TenantIsolation_EventBus(t *testing.T) {
	bus := newInProcessEventBus()
	chA, cancelA := bus.Subscribe(context.Background(), "chat-A")
	defer cancelA()
	chB, cancelB := bus.Subscribe(context.Background(), "chat-B")
	defer cancelB()

	bus.Publish(context.Background(), "chat-A", GenerationEvent{
		Type: EventTypeDone, GenerationID: "ga", ChatID: "chat-A",
	})
	select {
	case <-chA:
	case <-time.After(time.Second):
		t.Fatal("tenant A missed event")
	}
	select {
	case ev := <-chB:
		t.Fatalf("tenant B saw A's event: %+v", ev)
	default:
	}
}

func TestPhase7_LargeThemeGrepBounded(t *testing.T) {
	files := map[string]string{"pages.json": "[]"}
	for i := 0; i < 800; i++ {
		files[fmt.Sprintf("pages/p%d.liquid", i)] = fmt.Sprintf("needle-%d content", i%3)
	}
	ts := newFakeThemeServer(t, files)
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}
	cache := themefs.NewCachingStore(svc.store, themefs.ThemeKey{TenantID: 1, ThemeSlug: "huge"})
	input, _ := json.Marshal(grepThemeInput{Pattern: "needle"})

	start := time.Now()
	out, err := svc.execGrepTheme(context.Background(), cache, testStoreAuth(), input, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if out == "" || out == "(no matches)" {
		t.Fatalf("expected matches, got %q", out[:min(80, len(out))])
	}
	st := cache.Stats()
	if st.Entries > 512 {
		t.Fatalf("cache entries unbounded: %d", st.Entries)
	}
	// Grep scans at most maxGrepFilesScanned searchable files.
	t.Logf("large theme grep: elapsed=%s cache_entries=%d cache_bytes=%d out_len=%d",
		elapsed, st.Entries, st.Bytes, len(out))
}

func TestPhase7_SameTenantQueueCap(t *testing.T) {
	conn := openTestDB(t)
	chatRepo := chat.NewRepository(conn)
	chatSvc := chat.NewService(chatRepo)
	buildRepo := NewRepository(conn)

	tenantID := uint64(time.Now().UnixNano())
	ctx := context.Background()
	ch, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxQueueDepth; i++ {
		if _, err := buildRepo.EnqueueGeneration(ctx, Generation{
			ID: uuid.NewString(), ChatID: ch.ID, TenantID: tenantID,
			Prompt: "p", ThemeSlug: "demo",
		}); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	if _, err := buildRepo.EnqueueGeneration(ctx, Generation{
		ID: uuid.NewString(), ChatID: ch.ID, TenantID: tenantID,
		Prompt: "overflow", ThemeSlug: "demo",
	}); err == nil || err != ErrQueueFull {
		t.Fatalf("expected ErrQueueFull, got %v", err)
	}
}

func TestPhase7_MultiTenantQueuesIsolated(t *testing.T) {
	conn := openTestDB(t)
	chatRepo := chat.NewRepository(conn)
	chatSvc := chat.NewService(chatRepo)
	buildRepo := NewRepository(conn)
	ctx := context.Background()

	var chats []chat.Chat
	for i := 0; i < 5; i++ {
		tenantID := uint64(time.Now().UnixNano()) + uint64(i)*1000
		ch, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
		if err != nil {
			t.Fatal(err)
		}
		chats = append(chats, ch)
		if _, err := buildRepo.EnqueueGeneration(ctx, Generation{
			ID: uuid.NewString(), ChatID: ch.ID, TenantID: tenantID,
			Prompt: "p", ThemeSlug: fmt.Sprintf("theme-%d", i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i, ch := range chats {
		pending, err := buildRepo.ListPending(ctx, ch.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) != 1 {
			t.Fatalf("chat %d pending=%d", i, len(pending))
		}
		if pending[0].ThemeSlug != fmt.Sprintf("theme-%d", i) {
			t.Fatalf("cross-tenant theme leak: got %q", pending[0].ThemeSlug)
		}
	}
}

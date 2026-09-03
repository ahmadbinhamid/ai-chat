package themebuild

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/themefs"

	"github.com/google/uuid"
)

// alwaysFailGenerator is a generator whose Generate call always errors,
// regardless of ctx — this test cares only about what doGenerate's defer
// does once retErr is non-nil, not about a real Claude call.
type alwaysFailGenerator struct{}

func (alwaysFailGenerator) Generate(context.Context, ai.ThemeContext, []ai.Turn, string, func(string), ai.ToolProgress, ai.ToolExecutor, ai.FileReader) (*ai.Result, error) {
	return nil, context.Canceled
}

func (alwaysFailGenerator) Summarize(context.Context, []ai.Turn) (string, error) {
	return "", nil
}

// TestDoGenerate_FailureEventStillWrittenOnAlreadyCanceledContext is the
// mandatory regression test for the emitCtx fix: before it, doGenerate's
// defer emitted the "failed" event using the same ctx doGenerate itself ran
// on, which is exactly the context that's dead when generateTimeout (or a
// canceled caller context) is the reason the generation failed in the
// first place — AppendGenerationEvent silently failed against a canceled
// context, so nothing ever landed in generation_events. This test drives
// doGenerate with an already-canceled context and asserts the "failed"
// event is written anyway.
func TestDoGenerate_FailureEventStillWrittenOnAlreadyCanceledContext(t *testing.T) {
	conn := openTestDB(t)
	chatRepo := chat.NewRepository(conn)
	chatSvc := chat.NewService(chatRepo)
	buildRepo := NewRepository(conn)

	// Every theme-file read 404s — doGenerate reads pages.json/defaults.json
	// via buildThemeContext before ever reaching the generator; a 404 is
	// themefs' own "doesn't exist yet" case, not an error, so this doesn't
	// itself fail the generation before the generator gets a chance to.
	storeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer storeServer.Close()

	svc := NewService(buildRepo, chatSvc, nil, themefs.NewStore(storeServer.URL), nil)
	svc.gen = alwaysFailGenerator{}

	// A fresh tenant ID per run — chats are keyed on (tenant_id, type), and
	// this test's own doGenerate call never reaches runGeneration's
	// EndGeneration (it calls doGenerate directly), so a fixed tenant would
	// leave "a generation is already in progress" for every run after the
	// first.
	tenantID := uint64(time.Now().UnixNano())
	ctx := context.Background()
	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}

	genID := uuid.NewString()
	if err := buildRepo.StartGeneration(ctx, genID, c.ID, tenantID); err != nil {
		t.Fatalf("StartGeneration failed: %v", err)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel() // already dead before doGenerate even starts

	in := GenerateInput{TenantID: tenantID, Token: "t", ThemeSlug: "test-theme", Prompt: "do something"}
	retErr := svc.doGenerate(canceledCtx, in, c, genID, nil)
	if retErr == nil {
		t.Fatal("expected doGenerate to return an error")
	}

	events, err := buildRepo.GetEventsSince(context.Background(), c.ID, 0)
	if err != nil {
		t.Fatalf("GetEventsSince failed: %v", err)
	}
	var sawFailed bool
	for _, ev := range events {
		if ev.Type == EventTypeFailed {
			sawFailed = true
			if len(ev.Payload) == 0 {
				t.Error("expected the failed event to carry a non-empty payload")
			}
		}
	}
	if !sawFailed {
		t.Fatalf("expected a %q event to be written despite the canceled context, got events: %+v", EventTypeFailed, events)
	}

	// Item 7: a merchant-visible chat message must also exist for this
	// failure, not just the WebSocket-only event.
	messages, err := chatSvc.ListMessagesForVerifiedChat(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("ListMessagesForVerifiedChat failed: %v", err)
	}
	var sawFailedMessage bool
	for _, m := range messages {
		if m.Status == chat.MessageStatusFailed {
			sawFailedMessage = true
			if m.Content == "" {
				t.Error("expected the failed assistant message to have non-empty content")
			}
		}
	}
	if !sawFailedMessage {
		t.Fatalf("expected a chat message with status %q to be recorded, got messages: %+v", chat.MessageStatusFailed, messages)
	}
}

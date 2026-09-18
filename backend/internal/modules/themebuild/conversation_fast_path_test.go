package themebuild

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/themefs"
)

// panicOnThemeAccessStore fails if any theme I/O happens — conversation
// fast path must never load the theme.
type panicOnThemeAccessStore struct{}

func (panicOnThemeAccessStore) ReadFile(context.Context, themefs.RequestAuth, string) (string, error) {
	panic("theme ReadFile must not run on conversation fast path")
}
func (panicOnThemeAccessStore) WriteFile(context.Context, themefs.RequestAuth, string, string, *themefs.PageMeta) error {
	panic("theme WriteFile must not run on conversation fast path")
}
func (panicOnThemeAccessStore) DeleteFile(context.Context, themefs.RequestAuth, string) error {
	panic("theme DeleteFile must not run on conversation fast path")
}
func (panicOnThemeAccessStore) ListFiles(context.Context, themefs.RequestAuth) ([]themefs.FileTreeEntry, error) {
	panic("theme ListFiles must not run on conversation fast path")
}

type countingGenerator struct {
	calls atomic.Int32
}

func (g *countingGenerator) Generate(context.Context, ai.ThemeContext, []ai.Turn, string, []ai.Image, func(string), ai.ToolProgress, ai.ToolExecutor, ai.FileReader) (*ai.Result, error) {
	g.calls.Add(1)
	return &ai.Result{Summary: "should not be called", AnsweredQuestion: true}, nil
}
func (g *countingGenerator) SupportsVision() bool { return false }
func (g *countingGenerator) Summarize(context.Context, []ai.Turn) (string, error) {
	return "", nil
}

func TestDoGenerate_ConversationFastPath_NoDeepSeekNoTheme(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &countingGenerator{}
	svc.gen = gen
	svc.store = panicOnThemeAccessStore{}

	tenantID := uint64(time.Now().UnixNano())
	outcome, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme",
		Prompt: "hi", Mode: "edit",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	waitForAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)

	if n := gen.calls.Load(); n != 0 {
		t.Fatalf("DeepSeek Generate called %d times; want 0", n)
	}

	messages, err := chatSvc.ListMessages(context.Background(), tenantID, outcome.Chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) < 2 {
		t.Fatalf("expected user + assistant, got %d messages", len(messages))
	}
	if messages[0].Role != chat.RoleUser || messages[0].Content != "hi" {
		t.Fatalf("expected user hi first, got %+v", messages[0])
	}
	var assistant *chat.Message
	for i := range messages {
		if messages[i].Role == chat.RoleAssistant {
			assistant = &messages[i]
			break
		}
	}
	if assistant == nil {
		t.Fatal("expected assistant reply")
	}
	if assistant.Content == "" || assistant.Status != chat.MessageStatusCompleted {
		t.Fatalf("assistant=%+v", assistant)
	}
	if ClassifyIntent("hi", "edit", false) != IntentConversation {
		t.Fatal("router regression")
	}
}

func TestDoGenerate_ConversationFastPath_Thanks(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &countingGenerator{}
	svc.gen = gen
	svc.store = panicOnThemeAccessStore{}

	tenantID := uint64(time.Now().UnixNano())
	outcome, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme",
		Prompt: "thanks", Mode: "edit",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	waitForAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)
	if gen.calls.Load() != 0 {
		t.Fatal("DeepSeek must not be called for thanks")
	}
}

func TestDoGenerate_ThemeEditStillCallsGenerator(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &countingGenerator{}
	svc.gen = gen
	// Leave default store from newQueueTestService — theme path needs it.

	tenantID := uint64(time.Now().UnixNano())
	outcome, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme",
		Prompt: "change the header design", Mode: "edit",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	waitForAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)
	if gen.calls.Load() == 0 {
		t.Fatal("theme edit must call DeepSeek")
	}
}

package themebuild

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/themefs"

	"github.com/google/uuid"
)

// neverCalledGenerator fails the test if Generate is ever invoked — proves the deterministic
// path makes zero model calls, not just that the returned *ai.Result looks right.
type neverCalledGenerator struct{ t *testing.T }

func (g neverCalledGenerator) Generate(context.Context, ai.ThemeContext, []ai.Turn, string, []ai.Image, func(string), ai.ToolProgress, ai.ToolExecutor, ai.FileReader) (*ai.Result, error) {
	g.t.Fatal("the model must not be called for a deterministic register/diagnose-existing-page request")
	return nil, nil
}

func (g neverCalledGenerator) Summarize(context.Context, []ai.Turn) (string, error) { return "", nil }

func (g neverCalledGenerator) SupportsVision() bool { return false }

// Driven through the real doGenerate, asserting the same event/message/apply-status shape a
// model-driven turn would produce — the deterministic path must not break the frontend's expectations.
func TestDoGenerate_RegisterExistingPage_MatchesNormalGenerationShape(t *testing.T) {
	conn := openTestDB(t)
	chatRepo := chat.NewRepository(conn)
	chatSvc := chat.NewService(chatRepo)
	buildRepo := NewRepository(conn)

	storeServer := newFakeThemeServer(t, map[string]string{
		"pages/pricing.liquid": "PRICING CONTENT",
		"pages.json":           `[]`,
	})
	defer storeServer.Close()

	svc := NewService(buildRepo, chatSvc, nil, themefs.NewStore(storeServer.URL), nil)
	svc.gen = neverCalledGenerator{t: t}

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

	in := GenerateInput{TenantID: tenantID, Token: "t", ThemeSlug: "test-theme", Prompt: "register the pricing page"}
	if err := svc.doGenerate(ctx, in, c, genID, nil); err != nil {
		t.Fatalf("doGenerate returned an error: %v", err)
	}

	events, err := buildRepo.GetEventsSince(ctx, c.ID, 0)
	if err != nil {
		t.Fatalf("GetEventsSince failed: %v", err)
	}
	var sawStarted, sawStaged, sawDone bool
	var stagedPaths []string
	for _, ev := range events {
		switch ev.Type {
		case EventTypeStarted:
			sawStarted = true
		case EventTypeStaged:
			sawStaged = true
			var payload struct {
				Paths []string `json:"paths"`
			}
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				t.Fatalf("unmarshal staged payload: %v", err)
			}
			stagedPaths = payload.Paths
		case EventTypeDone:
			sawDone = true
		}
	}
	if !sawStarted || !sawStaged || !sawDone {
		t.Fatalf("expected started+staged+done events, got: %+v", events)
	}
	if len(stagedPaths) != 1 || stagedPaths[0] != "pages/pricing.liquid" {
		t.Errorf("expected staged paths [pages/pricing.liquid], got %v", stagedPaths)
	}

	messages, err := chatSvc.ListMessagesForVerifiedChat(ctx, c.ID)
	if err != nil {
		t.Fatalf("ListMessagesForVerifiedChat failed: %v", err)
	}
	var assistantMsg *chat.Message
	for i := range messages {
		if messages[i].Role == chat.RoleAssistant {
			assistantMsg = &messages[i]
		}
	}
	if assistantMsg == nil {
		t.Fatal("expected an assistant message to be recorded")
	}
	if assistantMsg.Status != chat.MessageStatusCompleted {
		t.Errorf("expected status %q, got %q", chat.MessageStatusCompleted, assistantMsg.Status)
	}
	if assistantMsg.ApplyStatus != chat.ApplyStatusPending {
		t.Errorf("expected apply status %q (a file was staged), got %q", chat.ApplyStatusPending, assistantMsg.ApplyStatus)
	}
	if !strings.Contains(assistantMsg.Content, "Pricing") {
		t.Errorf("expected the summary to mention the page, got %q", assistantMsg.Content)
	}
}

// No files staged, but still a completed message with ApplyStatusNotApplicable, like a normal
// "just answered a question" turn.
func TestDoGenerate_DiagnoseExistingPage_NoChangesButStillCompletes(t *testing.T) {
	conn := openTestDB(t)
	chatRepo := chat.NewRepository(conn)
	chatSvc := chat.NewService(chatRepo)
	buildRepo := NewRepository(conn)

	storeServer := newFakeThemeServer(t, map[string]string{
		"pages/pricing.liquid": "PRICING CONTENT",
		"pages.json":           `[{"slug":"pricing","page":"pricing","status":"draft"}]`,
	})
	defer storeServer.Close()

	svc := NewService(buildRepo, chatSvc, nil, themefs.NewStore(storeServer.URL), nil)
	svc.gen = neverCalledGenerator{t: t}

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

	in := GenerateInput{TenantID: tenantID, Token: "t", ThemeSlug: "test-theme", Prompt: "the pricing page is not working"}
	if err := svc.doGenerate(ctx, in, c, genID, nil); err != nil {
		t.Fatalf("doGenerate returned an error: %v", err)
	}

	events, err := buildRepo.GetEventsSince(ctx, c.ID, 0)
	if err != nil {
		t.Fatalf("GetEventsSince failed: %v", err)
	}
	var sawDone, sawStaged bool
	for _, ev := range events {
		if ev.Type == EventTypeDone {
			sawDone = true
		}
		if ev.Type == EventTypeStaged {
			sawStaged = true
		}
	}
	if !sawDone {
		t.Fatalf("expected a done event, got: %+v", events)
	}
	if sawStaged {
		t.Error("diagnose must never stage a file change")
	}

	messages, err := chatSvc.ListMessagesForVerifiedChat(ctx, c.ID)
	if err != nil {
		t.Fatalf("ListMessagesForVerifiedChat failed: %v", err)
	}
	var assistantMsg *chat.Message
	for i := range messages {
		if messages[i].Role == chat.RoleAssistant {
			assistantMsg = &messages[i]
		}
	}
	if assistantMsg == nil {
		t.Fatal("expected an assistant message to be recorded")
	}
	if assistantMsg.ApplyStatus != chat.ApplyStatusNotApplicable {
		t.Errorf("expected apply status %q, got %q", chat.ApplyStatusNotApplicable, assistantMsg.ApplyStatus)
	}
	if !strings.Contains(assistantMsg.Content, "not \"published\"") {
		t.Errorf("expected the diagnosis to mention the draft status, got %q", assistantMsg.Content)
	}
}

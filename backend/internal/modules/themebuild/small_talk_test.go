package themebuild

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/previewerrors"
	"ai-chat/internal/themefs"
)

func TestSmallTalkReply(t *testing.T) {
	html := "<p>ref</p>"
	tests := []struct {
		name  string
		in    GenerateInput
		prior []chat.Message
		want  string // "" = falls through
	}{
		{"greeting", GenerateInput{Prompt: "hi"}, nil, greetingReply},
		{"acknowledgement", GenerateInput{Prompt: "thanks"}, nil, acknowledgementReply},
		{"real request", GenerateInput{Prompt: "hi, can you make the header blue"}, nil, ""},
		{"with an image", GenerateInput{Prompt: "hi", Images: []chat.MessageImage{{Base64: "AA==", MediaType: "image/png"}}}, nil, ""},
		{"with this turn's HTML attachment", GenerateInput{Prompt: "hi", HTMLAttachmentContent: &html}, nil, ""},
		{"with a carried-forward HTML attachment", GenerateInput{Prompt: "thanks", HTMLAttachmentContent: &html, HTMLAttachmentCarriedForward: true}, nil, acknowledgementReply},
		{"with a reference URL", GenerateInput{Prompt: "hi", ReferenceURL: "https://example.com"}, nil, ""},
		{"with preview errors", GenerateInput{Prompt: "hi", PreviewErrors: []previewerrors.Entry{{Type: "error", Message: "boom", Count: 1}}}, nil, ""},
		{"with a mode", GenerateInput{Prompt: "hi", Mode: "brand"}, nil, ""},
		{"explicit edit mode is the default", GenerateInput{Prompt: "hi", Mode: "edit"}, nil, greetingReply},
		{"answering an assistant question", GenerateInput{Prompt: "ok"}, []chat.Message{
			{Role: chat.RoleUser, Content: "update the header"},
			{Role: chat.RoleAssistant, Status: chat.MessageStatusCompleted, Content: "Done. Want me to update the footer too?"},
			{Role: chat.RoleUser, Content: "ok"},
		}, ""},
		{"after a statement", GenerateInput{Prompt: "thanks"}, []chat.Message{
			{Role: chat.RoleAssistant, Status: chat.MessageStatusCompleted, Content: "I've updated the header."},
		}, acknowledgementReply},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := smallTalkReply(tt.in, tt.prior)
			if tt.want == "" {
				if ok {
					t.Fatalf("expected fall-through, got %+v", result)
				}
				return
			}
			if !ok || result.Summary != tt.want || !result.AnsweredQuestion || len(result.Files) != 0 {
				t.Fatalf("got %+v (ok=%v), want summary %q", result, ok, tt.want)
			}
		})
	}
}

// answeringGenerator stands in for a normal model turn that answers without changing files.
type answeringGenerator struct{}

func (answeringGenerator) Generate(context.Context, ai.ThemeContext, []ai.Turn, string, []ai.Image, ai.ToolProgress, ai.ToolExecutor, ai.FileReader) (*ai.Result, error) {
	return &ai.Result{Summary: "An answer.", AnsweredQuestion: true}, nil
}
func (answeringGenerator) SupportsVision() bool                                 { return false }
func (answeringGenerator) Summarize(context.Context, string, []ai.Turn) (string, error) { return "", nil }

// newCountingStoreService is newQueueTestService with a FlowPOS fake that counts every request.
func newCountingStoreService(t *testing.T) (*Service, *chat.Service, *atomic.Int64) {
	t.Helper()
	conn := openTestDB(t)
	chatSvc := chat.NewService(chat.NewRepository(conn))
	var calls atomic.Int64
	storeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/store/themes/active/files" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"files":[]}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(storeServer.Close)
	return NewService(NewRepository(conn), chatSvc, nil, themefs.NewStore(storeServer.URL), nil), chatSvc, &calls
}

func eventTypes(t *testing.T, svc *Service, chatID string) []string {
	t.Helper()
	events, err := svc.repo.GetEventsSince(context.Background(), chatID, 0)
	if err != nil {
		t.Fatalf("GetEventsSince: %v", err)
	}
	types := make([]string, len(events))
	for i, e := range events {
		types[i] = e.Type
	}
	return types
}

func TestDoGenerate_GreetingSkipsModelAndFlowPOS(t *testing.T) {
	svc, chatSvc, storeCalls := newCountingStoreService(t)
	svc.gen = neverCalledGenerator{t: t}
	tenantID := uint64(time.Now().UnixNano())

	outcome, err := svc.Generate(context.Background(), GenerateInput{TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme", Prompt: "Hello!"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	waitForAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)

	if n := storeCalls.Load(); n != 0 {
		t.Errorf("expected zero FlowPOS calls (buildThemeContext skipped), got %d", n)
	}
	messages, err := chatSvc.ListMessages(context.Background(), tenantID, outcome.Chat.ID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	reply := messages[len(messages)-1]
	if reply.Role != chat.RoleAssistant || reply.Content != greetingReply || reply.Status != chat.MessageStatusCompleted ||
		reply.ApplyStatus != chat.ApplyStatusNotApplicable {
		t.Errorf("unexpected reply message %+v", reply)
	}
	draft, err := svc.repo.DraftFiles(context.Background(), outcome.Chat.ID)
	if err != nil || len(draft) != 0 {
		t.Errorf("expected the draft untouched, got %v (%v)", draft, err)
	}

	// Same events as a normal turn that answers without changing files.
	normalSvc, normalChats, _ := newCountingStoreService(t)
	normalSvc.gen = answeringGenerator{}
	otherTenant := tenantID + 1
	normal, err := normalSvc.Generate(context.Background(), GenerateInput{TenantID: otherTenant, UserID: &otherTenant, Token: "t", ThemeSlug: "theme", Prompt: "what can you do?"})
	if err != nil {
		t.Fatalf("normal Generate: %v", err)
	}
	waitForAssistantReply(t, normalChats, otherTenant, normal.Chat.ID)

	greetingEvents, normalEvents := eventTypes(t, svc, outcome.Chat.ID), eventTypes(t, normalSvc, normal.Chat.ID)
	if len(greetingEvents) == 0 || len(greetingEvents) != len(normalEvents) {
		t.Fatalf("event sequences differ: greeting %v, normal %v", greetingEvents, normalEvents)
	}
	for i := range greetingEvents {
		if greetingEvents[i] != normalEvents[i] {
			t.Errorf("event %d: greeting %q, normal %q", i, greetingEvents[i], normalEvents[i])
		}
	}
}

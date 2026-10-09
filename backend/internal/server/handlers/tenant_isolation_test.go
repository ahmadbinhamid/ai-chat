package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-chat/internal/auth"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/modules/themebuild"
	"ai-chat/internal/themefs"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// tenantAFixture is a real chat for tenant A with messages, an image attachment, a pending draft file and, when
// asked, a running plus a queued generation.
type tenantAFixture struct {
	tenantID     uint64
	chat         chat.Chat
	userMsg      chat.Message
	assistantMsg chat.Message
	attachmentID string
	runningGenID string
	queuedGenID  string
}

const isolationDraftPath = "components/css/header.css"

func seedTenantA(t *testing.T, chatSvc *chat.Service, buildRepo *themebuild.Repository, withGenerations bool) tenantAFixture {
	t.Helper()
	ctx := context.Background()
	f := tenantAFixture{tenantID: uint64(time.Now().UnixNano())}
	var err error
	if f.chat, err = chatSvc.GetOrCreateChat(ctx, f.tenantID, themebuild.ChatType); err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}
	userID := uint64(1)
	f.userMsg, err = chatSvc.RecordUserMessage(ctx, f.chat, &userID, "", "", "make the header dark",
		[]chat.MessageImage{{Base64: "QUFBQQ==", MediaType: "image/png"}}, nil, nil, nil)
	if err != nil {
		t.Fatalf("RecordUserMessage failed: %v", err)
	}
	f.attachmentID = f.userMsg.Attachments[0].ID

	tx, err := buildRepo.BeginTx(ctx)
	if err != nil {
		t.Fatalf("BeginTx failed: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	f.assistantMsg, err = chatSvc.RecordAssistantMessageInTx(ctx, tx, f.chat, "Made the header dark.", chat.MessageStatusCompleted,
		0, 0, chat.ApplyStatusPending, "", "", nil)
	if err != nil {
		t.Fatalf("RecordAssistantMessageInTx failed: %v", err)
	}
	if err := buildRepo.CreateFileTx(ctx, tx, themebuild.GeneratedFile{
		ID: uuid.NewString(), MessageID: f.assistantMsg.ID, ChatID: f.chat.ID, FilePath: isolationDraftPath,
		Action: themebuild.FileActionUpdate, Kind: themebuild.GeneratedFileKindProposed, Language: "css",
		Content: "header { background: #000; }", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateFileTx failed: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit fixture: %v", err)
	}

	if withGenerations {
		f.runningGenID = uuid.NewString()
		if err := buildRepo.StartGeneration(ctx, f.runningGenID, f.chat.ID, f.tenantID); err != nil {
			t.Fatalf("StartGeneration failed: %v", err)
		}
		f.queuedGenID = uuid.NewString()
		if _, err := buildRepo.EnqueueGeneration(ctx, themebuild.Generation{
			ID: f.queuedGenID, ChatID: f.chat.ID, TenantID: f.tenantID, Prompt: "and the footer", ThemeSlug: "shop",
		}); err != nil {
			t.Fatalf("EnqueueGeneration failed: %v", err)
		}
		t.Cleanup(func() {
			_ = buildRepo.CancelQueued(context.Background(), f.chat.ID, f.queuedGenID)
			_ = buildRepo.EndGeneration(context.Background(), f.chat.ID, f.runningGenID, nil)
		})
	}
	return f
}

type generationState struct {
	ID              string
	Status          string
	CancelRequested bool
}

type messageState struct {
	ID, Content         string
	Status, ApplyStatus string
}

// tenantASnapshot is every row a cross-tenant call could plausibly touch.
type tenantASnapshot struct {
	Generations []generationState
	Draft       map[string]string
	Messages    []messageState
	EventCount  int
}

func snapshotTenantA(t *testing.T, chatSvc *chat.Service, buildRepo *themebuild.Repository, f tenantAFixture) tenantASnapshot {
	t.Helper()
	ctx := context.Background()
	var s tenantASnapshot
	for _, id := range []string{f.runningGenID, f.queuedGenID} {
		if id == "" {
			continue
		}
		g, err := buildRepo.GetGenerationByID(ctx, f.chat.ID, id)
		if err != nil {
			t.Fatalf("GetGenerationByID failed: %v", err)
		}
		requested, err := buildRepo.IsCancellationRequested(ctx, f.chat.ID, id)
		if err != nil {
			t.Fatalf("IsCancellationRequested failed: %v", err)
		}
		s.Generations = append(s.Generations, generationState{ID: g.ID, Status: g.Status, CancelRequested: requested})
	}
	var err error
	if s.Draft, err = buildRepo.DraftFiles(ctx, f.chat.ID); err != nil {
		t.Fatalf("DraftFiles failed: %v", err)
	}
	if len(s.Draft) == 0 {
		t.Fatal("fixture has no draft file; the draft assertion would prove nothing")
	}
	messages, err := chatSvc.ListMessagesForVerifiedChat(ctx, f.chat.ID)
	if err != nil {
		t.Fatalf("ListMessagesForVerifiedChat failed: %v", err)
	}
	for _, m := range messages {
		s.Messages = append(s.Messages, messageState{ID: m.ID, Content: m.Content, Status: string(m.Status), ApplyStatus: string(m.ApplyStatus)})
	}
	events, err := buildRepo.GetEventsSince(ctx, f.chat.ID, 0)
	if err != nil {
		t.Fatalf("GetEventsSince failed: %v", err)
	}
	s.EventCount = len(events)
	return s
}

// countingStore stands in for flowpos-backend's theme API; any call means a request got past the ownership check.
func countingStore(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// Tenant B, authenticated, sends tenant A's real IDs to every chat-scoped route: each must 404 (never 403, which
// would confirm the resource exists) and leave every one of A's rows exactly as it was.
func TestTenantIsolation_OtherTenantGets404AndChangesNothing(t *testing.T) {
	tests := []struct {
		name            string
		method          string
		path            func(f tenantAFixture) string
		body            string
		withGenerations bool
	}{
		{"revert", http.MethodPost, func(f tenantAFixture) string {
			return "/chats/" + f.chat.ID + "/messages/" + f.assistantMsg.ID + "/revert"
		}, "", false},
		{"attachment", http.MethodGet, func(f tenantAFixture) string {
			return "/chats/" + f.chat.ID + "/messages/" + f.userMsg.ID + "/attachments/" + f.attachmentID
		}, "", false},
		{"cancel running generation", http.MethodDelete, func(f tenantAFixture) string {
			return "/chats/" + f.chat.ID + "/queue/" + f.runningGenID
		}, "", true},
		{"cancel queued generation", http.MethodDelete, func(f tenantAFixture) string {
			return "/chats/" + f.chat.ID + "/queue/" + f.queuedGenID
		}, "", true},
		{"cancel whole queue", http.MethodDelete, func(f tenantAFixture) string {
			return "/chats/" + f.chat.ID + "/queue"
		}, "", true},
		{"apply", http.MethodPost, func(f tenantAFixture) string {
			return "/chats/" + f.chat.ID + "/apply"
		}, `{"theme_slug":"shop"}`, false},
		{"discard", http.MethodPost, func(f tenantAFixture) string {
			return "/chats/" + f.chat.ID + "/discard"
		}, "", false},
		{"draft files", http.MethodGet, func(f tenantAFixture) string {
			return "/chats/" + f.chat.ID + "/draft"
		}, "", false},
		{"draft manual edit", http.MethodPost, func(f tenantAFixture) string {
			return "/chats/" + f.chat.ID + "/draft/edit"
		}, `{"file_path":"` + isolationDraftPath + `","content":"header { color: red; }"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := openStreamTestDB(t)
			chatSvc := chat.NewService(chat.NewRepository(conn))
			buildRepo := themebuild.NewRepository(conn)
			store, storeCalls := countingStore(t)
			buildSvc := themebuild.NewService(buildRepo, chatSvc, nil, themefs.NewStore(store.URL), nil)

			a := seedTenantA(t, chatSvc, buildRepo, tt.withGenerations)
			before := snapshotTenantA(t, chatSvc, buildRepo, a)

			router := isolationRouter(chatSvc, buildSvc, a.tenantID+1)
			req := httptest.NewRequest(tt.method, tt.path(a), strings.NewReader(tt.body))
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("tenant B got %d on tenant A's %s, want 404: %s", rec.Code, tt.name, rec.Body.String())
			}
			assertTenantAUnchanged(t, chatSvc, buildRepo, a, before, storeCalls)

			// Control: the same request from A reaches the resource, so B's 404 came from the ownership check.
			ownReq := httptest.NewRequest(tt.method, tt.path(a), strings.NewReader(tt.body))
			if tt.body != "" {
				ownReq.Header.Set("Content-Type", "application/json")
			}
			ownRec := httptest.NewRecorder()
			isolationRouter(chatSvc, buildSvc, a.tenantID).ServeHTTP(ownRec, ownReq)
			if ownRec.Code == http.StatusNotFound {
				t.Fatalf("tenant A's own %s also 404s, so the route or fixture is wrong: %s", tt.name, ownRec.Body.String())
			}
		})
	}
}

// GET /chat/status takes no chat ID (it resolves the caller's own chat), so B can't aim it at A's chat; it must
// report B's own idle state and nothing of A's running generation.
func TestTenantIsolation_StatusReportsOnlyOwnChat(t *testing.T) {
	conn := openStreamTestDB(t)
	chatSvc := chat.NewService(chat.NewRepository(conn))
	buildRepo := themebuild.NewRepository(conn)
	store, storeCalls := countingStore(t)
	buildSvc := themebuild.NewService(buildRepo, chatSvc, nil, themefs.NewStore(store.URL), nil)

	a := seedTenantA(t, chatSvc, buildRepo, true)
	before := snapshotTenantA(t, chatSvc, buildRepo, a)

	for _, bHasChat := range []bool{false, true} {
		tenantB := a.tenantID + 1
		if bHasChat {
			tenantB = a.tenantID + 2
			if _, err := chatSvc.GetOrCreateChat(context.Background(), tenantB, themebuild.ChatType); err != nil {
				t.Fatalf("GetOrCreateChat for B failed: %v", err)
			}
		}
		rec := httptest.NewRecorder()
		isolationRouter(chatSvc, buildSvc, tenantB).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/chat/status", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("B (has chat: %v) got %d from /chat/status: %s", bHasChat, rec.Code, rec.Body.String())
		}
		var body struct {
			Data struct {
				Generating bool `json:"generating"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode status: %v (%s)", err, rec.Body.String())
		}
		if body.Data.Generating || strings.Contains(rec.Body.String(), a.chat.ID) {
			t.Fatalf("B (has chat: %v) saw tenant A's generation state: %s", bHasChat, rec.Body.String())
		}
	}
	assertTenantAUnchanged(t, chatSvc, buildRepo, a, before, storeCalls)
}

func TestTenantIsolation_StreamOtherTenantGets404(t *testing.T) {
	conn := openStreamTestDB(t)
	chatSvc := chat.NewService(chat.NewRepository(conn))
	buildRepo := themebuild.NewRepository(conn)
	store, storeCalls := countingStore(t)
	buildSvc := themebuild.NewService(buildRepo, chatSvc, nil, themefs.NewStore(store.URL), nil)

	a := seedTenantA(t, chatSvc, buildRepo, true)
	mustAppendEvent(t, context.Background(), buildRepo, a.runningGenID, a.chat.ID, 1, themebuild.EventTypeStarted, struct{}{})
	before := snapshotTenantA(t, chatSvc, buildRepo, a)

	tenantB := a.tenantID + 1
	flowpos := fakeFlowposServer(t, tenantB)
	authCache := auth.NewMemoryCache()
	t.Cleanup(authCache.Close)
	router := gin.New()
	router.GET("/chats/:chatId/stream", NewStreamHandler(chatSvc, buildSvc, auth.NewClient(flowpos.URL, 5*time.Second), authCache, time.Minute, time.Minute, nil).Stream)
	ts := httptest.NewServer(router)
	defer ts.Close()

	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/chats/" + a.chat.ID + "/stream"
	wsConn, resp, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{ //nolint:bodyclose // Dial closes resp.Body internally.
		Subprotocols: authSubprotocols("tenant-b-token", tenantB),
	})
	if err == nil {
		_ = wsConn.CloseNow()
		t.Fatal("tenant B opened a stream on tenant A's chat")
	}
	if resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected the handshake to fail with 404, got %+v (err %v)", resp, err)
	}
	assertTenantAUnchanged(t, chatSvc, buildRepo, a, before, storeCalls)
}

func isolationRouter(chatSvc *chat.Service, buildSvc *themebuild.Service, tenantB uint64) *gin.Engine {
	router := gin.New()
	router.Use(fakeAuthMiddleware(tenantB))
	queue := NewQueueHandler(buildSvc)
	apply := NewApplyHandler(buildSvc)
	draft := NewDraftHandler(buildSvc)
	router.GET("/chat/status", NewChatHandler(chatSvc, buildSvc).Status)
	router.POST("/chats/:chatId/messages/:messageId/revert", NewRevertHandler(buildSvc).Revert)
	router.GET("/chats/:chatId/messages/:messageId/attachments/:attachmentId", NewAttachmentHandler(buildSvc).Get)
	router.DELETE("/chats/:chatId/queue/:generationId", queue.Cancel)
	router.DELETE("/chats/:chatId/queue", queue.CancelAll)
	router.POST("/chats/:chatId/apply", apply.Apply)
	router.POST("/chats/:chatId/discard", apply.Discard)
	router.GET("/chats/:chatId/draft", draft.Files)
	router.POST("/chats/:chatId/draft/edit", draft.SaveManualEdit)
	return router
}

func assertTenantAUnchanged(t *testing.T, chatSvc *chat.Service, buildRepo *themebuild.Repository, a tenantAFixture, before tenantASnapshot, storeCalls *atomic.Int32) {
	t.Helper()
	after := snapshotTenantA(t, chatSvc, buildRepo, a)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("tenant A's rows changed:\nbefore %+v\nafter  %+v", before, after)
	}
	if n := storeCalls.Load(); n != 0 {
		t.Errorf("tenant B's request reached the theme store %d times", n)
	}
}

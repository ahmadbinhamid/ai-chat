package themebuild

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-chat/internal/modules/chat"
	"ai-chat/internal/themefs"

	"github.com/google/uuid"
)

// fakeRevertThemeServer is an in-memory stand-in for flowpos-backend's
// theme-file API, tracking current file content so the test can assert on
// what RevertToMessage actually wrote/deleted through themefs.Store — the
// real HTTP boundary this service always goes through (see themefs.Store's
// own doc comment on why there's no local disk to inspect directly).
type fakeRevertThemeServer struct {
	mu    sync.Mutex
	files map[string]string
}

func newFakeRevertThemeServer() *fakeRevertThemeServer {
	return &fakeRevertThemeServer{files: map[string]string{}}
}

func (f *fakeRevertThemeServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/store/themes/active/files/")
		f.mu.Lock()
		defer f.mu.Unlock()

		switch r.Method {
		case http.MethodGet:
			content, ok := f.files[path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"path": path, "content": content, "encoding": "utf-8"}})
		case http.MethodPost:
			var body struct {
				Content string `json:"content"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.files[path] = body.Content
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			if _, ok := f.files[path]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			delete(f.files, path)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

func (f *fakeRevertThemeServer) get(path string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.files[path]
	return v, ok
}

func TestRevertToMessage_RestoresEditedFileAndDeletesNewerOne(t *testing.T) {
	dbConn := openTestDB(t)
	chatRepo := chat.NewRepository(dbConn)
	chatSvc := chat.NewService(chatRepo)
	buildRepo := NewRepository(dbConn)

	fakeServer := newFakeRevertThemeServer()
	ts := httptest.NewServer(fakeServer.handler())
	defer ts.Close()
	store := themefs.NewStore(ts.URL)

	svc := NewService(buildRepo, chatSvc, nil, store, nil)

	ctx := context.Background()
	tenantID := uint64(time.Now().UnixNano())
	const token = "test-token"

	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}

	// Turn 1: create pages/offers.liquid with "v1".
	fakeServer.mu.Lock()
	fakeServer.files["pages/offers.liquid"] = "v1"
	fakeServer.mu.Unlock()
	msg1, err := chatSvc.RecordAssistantMessage(ctx, c, "created offers page", chat.MessageStatusCompleted, 10, 10, chat.ApplyStatusApplied)
	if err != nil {
		t.Fatalf("RecordAssistantMessage (turn 1) failed: %v", err)
	}
	if err := buildRepo.CreateFile(ctx, GeneratedFile{
		ID: uuid.NewString(), MessageID: msg1.ID, ChatID: c.ID, FilePath: "pages/offers.liquid",
		Action: FileActionCreate, Language: "LIQUID", Content: "v1", CreatedAt: msg1.CreatedAt, UpdatedAt: msg1.CreatedAt,
	}); err != nil {
		t.Fatalf("CreateFile (turn 1) failed: %v", err)
	}

	time.Sleep(1100 * time.Millisecond) // ensure strictly increasing created_at across turns

	// Turn 2: edit pages/offers.liquid to "v2".
	fakeServer.mu.Lock()
	fakeServer.files["pages/offers.liquid"] = "v2"
	fakeServer.mu.Unlock()
	msg2, err := chatSvc.RecordAssistantMessage(ctx, c, "edited offers page", chat.MessageStatusCompleted, 10, 10, chat.ApplyStatusApplied)
	if err != nil {
		t.Fatalf("RecordAssistantMessage (turn 2) failed: %v", err)
	}
	prev := "v1"
	if err := buildRepo.CreateFile(ctx, GeneratedFile{
		ID: uuid.NewString(), MessageID: msg2.ID, ChatID: c.ID, FilePath: "pages/offers.liquid",
		Action: FileActionUpdate, Language: "LIQUID", Content: "v2", PreviousContent: &prev,
		CreatedAt: msg2.CreatedAt, UpdatedAt: msg2.CreatedAt,
	}); err != nil {
		t.Fatalf("CreateFile (turn 2) failed: %v", err)
	}

	time.Sleep(1100 * time.Millisecond)

	// Turn 3: create a brand-new file, pages/deals.liquid.
	fakeServer.mu.Lock()
	fakeServer.files["pages/deals.liquid"] = "deals content"
	fakeServer.mu.Unlock()
	msg3, err := chatSvc.RecordAssistantMessage(ctx, c, "created deals page", chat.MessageStatusCompleted, 10, 10, chat.ApplyStatusApplied)
	if err != nil {
		t.Fatalf("RecordAssistantMessage (turn 3) failed: %v", err)
	}
	if err := buildRepo.CreateFile(ctx, GeneratedFile{
		ID: uuid.NewString(), MessageID: msg3.ID, ChatID: c.ID, FilePath: "pages/deals.liquid",
		Action: FileActionCreate, Language: "LIQUID", Content: "deals content", CreatedAt: msg3.CreatedAt, UpdatedAt: msg3.CreatedAt,
	}); err != nil {
		t.Fatalf("CreateFile (turn 3) failed: %v", err)
	}

	// Revert to turn 1: offers.liquid should go back to "v1", deals.liquid
	// (which didn't exist at turn 1) should be deleted entirely.
	result, err := svc.RevertToMessage(ctx, tenantID, token, c.ID, msg1.ID)
	if err != nil {
		t.Fatalf("RevertToMessage failed: %v", err)
	}

	if len(result.RestoredFiles) != 1 || result.RestoredFiles[0] != "pages/offers.liquid" {
		t.Errorf("expected pages/offers.liquid restored, got %+v", result.RestoredFiles)
	}
	if len(result.DeletedFiles) != 1 || result.DeletedFiles[0] != "pages/deals.liquid" {
		t.Errorf("expected pages/deals.liquid deleted, got %+v", result.DeletedFiles)
	}

	if got, ok := fakeServer.get("pages/offers.liquid"); !ok || got != "v1" {
		t.Errorf("expected pages/offers.liquid to be restored to %q, got %q (exists=%v)", "v1", got, ok)
	}
	if _, ok := fakeServer.get("pages/deals.liquid"); ok {
		t.Error("expected pages/deals.liquid to have been deleted")
	}
}

func TestRevertToMessage_BlockedByRunningGeneration(t *testing.T) {
	dbConn := openTestDB(t)
	chatRepo := chat.NewRepository(dbConn)
	chatSvc := chat.NewService(chatRepo)
	buildRepo := NewRepository(dbConn)
	svc := NewService(buildRepo, chatSvc, nil, themefs.NewStore("http://unused.invalid"), nil)

	ctx := context.Background()
	tenantID := uint64(time.Now().UnixNano())
	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}
	msg, err := chatSvc.RecordAssistantMessage(ctx, c, "did something", chat.MessageStatusCompleted, 1, 1, chat.ApplyStatusApplied)
	if err != nil {
		t.Fatalf("RecordAssistantMessage failed: %v", err)
	}
	if err := buildRepo.StartGeneration(ctx, uuid.NewString(), c.ID, tenantID); err != nil {
		t.Fatalf("StartGeneration failed: %v", err)
	}

	if _, err := svc.RevertToMessage(ctx, tenantID, "token", c.ID, msg.ID); !errors.Is(err, ErrRevertBlockedByRunningGeneration) {
		t.Fatalf("expected ErrRevertBlockedByRunningGeneration, got %v", err)
	}
}

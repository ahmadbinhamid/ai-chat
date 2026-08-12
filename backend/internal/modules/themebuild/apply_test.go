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

// fakeApplyServer stands in for flowpos-backend's theme-file API for
// ApplyDraft/DiscardDraft/RevertToMessage tests — every GET 404s (nothing
// pre-exists), every POST (write) is recorded, and calls is a running
// count of every request this server ever received, useful for "zero
// FlowPOS calls" assertions (items 10, 12).
type fakeApplyServer struct {
	mu       sync.Mutex
	calls    int
	writes   map[string]int            // path -> write count
	bodies   map[string]map[string]any // path -> last decoded request body
	failPath string                    // if set, POST to this path 500s
}

func newFakeApplyServer() *fakeApplyServer {
	return &fakeApplyServer{writes: make(map[string]int), bodies: make(map[string]map[string]any)}
}

func (f *fakeApplyServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls++
		f.mu.Unlock()

		reqPath := strings.TrimPrefix(r.URL.Path, "/store/themes/active/files/")
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPost:
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.failPath != "" && reqPath == f.failPath {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.writes[reqPath]++
			f.bodies[reqPath] = body
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

func (f *fakeApplyServer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// newApplyTestService wires a real DB-backed chat/themebuild Service
// against fakeApplyServer — the same shape revert_test.go's own tests use.
func newApplyTestService(t *testing.T, fake *fakeApplyServer) (*Service, *chat.Service, *Repository) {
	t.Helper()
	conn := openTestDB(t)
	chatSvc := chat.NewService(chat.NewRepository(conn))
	buildRepo := NewRepository(conn)
	ts := httptest.NewServer(fake.handler())
	t.Cleanup(ts.Close)
	svc := NewService(buildRepo, chatSvc, nil, themefs.NewStore(ts.URL), nil)
	return svc, chatSvc, buildRepo
}

// Item 5: ApplyDraft writes every pending path exactly once and marks
// messages applied.
func TestApplyDraft_WritesEveryPendingPathOnceAndMarksApplied(t *testing.T) {
	fake := newFakeApplyServer()
	svc, chatSvc, buildRepo := newApplyTestService(t, fake)
	ctx := context.Background()
	tenantID := uint64(time.Now().UnixNano())

	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}
	seedPendingFile(t, chatSvc, buildRepo, c, "pages/home.liquid", "hi", GeneratedFileKindProposed)
	seedPendingFile(t, chatSvc, buildRepo, c, "pages/about.liquid", "there", GeneratedFileKindProposed)

	result, err := svc.ApplyDraft(ctx, tenantID, "tok", c.ID, "demo-theme")
	if err != nil {
		t.Fatalf("ApplyDraft failed: %v", err)
	}
	if len(result.AppliedPaths) != 2 {
		t.Fatalf("expected 2 applied paths, got %+v", result.AppliedPaths)
	}
	if fake.writes["pages/home.liquid"] != 1 || fake.writes["pages/about.liquid"] != 1 {
		t.Fatalf("expected each pending path written exactly once, got %+v", fake.writes)
	}

	messages, err := chatSvc.ListMessagesForVerifiedChat(ctx, c.ID)
	if err != nil {
		t.Fatalf("ListMessagesForVerifiedChat failed: %v", err)
	}
	for _, m := range messages {
		if m.Role != chat.RoleAssistant {
			continue
		}
		if m.ApplyStatus != chat.ApplyStatusApplied || m.AppliedAt == nil {
			t.Errorf("expected message %s applied with AppliedAt set, got %+v", m.ID, m)
		}
	}
}

// Item 6: ApplyDraft is rejected while a generation is running or queued.
func TestApplyDraft_RejectedWhileGenerationRunningOrQueued(t *testing.T) {
	fake := newFakeApplyServer()
	svc, chatSvc, buildRepo := newApplyTestService(t, fake)
	ctx := context.Background()
	tenantID := uint64(time.Now().UnixNano())

	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}
	seedPendingFile(t, chatSvc, buildRepo, c, "pages/home.liquid", "hi", GeneratedFileKindProposed)

	if err := buildRepo.StartGeneration(ctx, uuid.NewString(), c.ID, tenantID); err != nil {
		t.Fatalf("StartGeneration failed: %v", err)
	}

	if _, err := svc.ApplyDraft(ctx, tenantID, "tok", c.ID, "demo-theme"); !errors.Is(err, ErrApplyBlockedByRunningGeneration) {
		t.Fatalf("expected ErrApplyBlockedByRunningGeneration, got %v", err)
	}
	if fake.callCount() != 0 {
		t.Fatalf("expected zero FlowPOS calls when blocked, got %d", fake.callCount())
	}
}

// Item 7: ApplyDraft restores PageMeta for a pages/*.liquid file — the
// model's own PageRegistryEntry is long gone by apply time; only the
// persisted page_meta column has it.
func TestApplyDraft_RestoresPageMetaForPagesFile(t *testing.T) {
	fake := newFakeApplyServer()
	svc, chatSvc, buildRepo := newApplyTestService(t, fake)
	ctx := context.Background()
	tenantID := uint64(time.Now().UnixNano())

	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}
	msg, err := chatSvc.RecordAssistantMessage(ctx, c, "turn", chat.MessageStatusCompleted, 0, 0, chat.ApplyStatusPending)
	if err != nil {
		t.Fatalf("RecordAssistantMessage failed: %v", err)
	}
	now := time.Now().UTC()
	if err := buildRepo.CreateFile(ctx, GeneratedFile{
		ID: uuid.NewString(), MessageID: msg.ID, ChatID: c.ID, FilePath: "pages/offers.liquid",
		Action: FileActionCreate, Kind: GeneratedFileKindProposed, Content: "<p>offers</p>",
		PageMeta:  &themefs.PageMeta{Title: "Offers", Slug: "offers", Type: "custom", Status: "draft"},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateFile failed: %v", err)
	}

	if _, err := svc.ApplyDraft(ctx, tenantID, "tok", c.ID, "demo-theme"); err != nil {
		t.Fatalf("ApplyDraft failed: %v", err)
	}

	body := fake.bodies["pages/offers.liquid"]
	if body == nil {
		t.Fatal("expected a write recorded for pages/offers.liquid")
	}
	if body["title"] != "Offers" || body["slug"] != "offers" {
		t.Fatalf("expected page meta restored on the write, got %+v", body)
	}
}

// Item 8: ApplyDraft applies a kind='layout' row so the <link> tag
// survives — a layout splice staged during generation but never separately
// audited before this feature would otherwise be lost between staging and
// apply.
func TestApplyDraft_AppliesLayoutRow(t *testing.T) {
	fake := newFakeApplyServer()
	svc, chatSvc, buildRepo := newApplyTestService(t, fake)
	ctx := context.Background()
	tenantID := uint64(time.Now().UnixNano())

	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}
	splicedLayout := `<link rel="stylesheet" href="/theme-assets/css/offers.css">`
	seedPendingFile(t, chatSvc, buildRepo, c, pathLayoutStart, splicedLayout, GeneratedFileKindLayout)

	if _, err := svc.ApplyDraft(ctx, tenantID, "tok", c.ID, "demo-theme"); err != nil {
		t.Fatalf("ApplyDraft failed: %v", err)
	}

	body := fake.bodies[pathLayoutStart]
	if body == nil {
		t.Fatalf("expected %s to have been written", pathLayoutStart)
	}
	if body["content"] != splicedLayout {
		t.Fatalf("expected the spliced layout content to survive to apply, got %+v", body)
	}
}

// Item 9: ApplyDraft partial failure leaves messages pending and names the
// failed path — a retryable partial beats a draft falsely marked applied.
func TestApplyDraft_PartialFailureLeavesMessagesPendingAndNamesPath(t *testing.T) {
	fake := newFakeApplyServer()
	fake.failPath = "pages/about.liquid"
	svc, chatSvc, buildRepo := newApplyTestService(t, fake)
	ctx := context.Background()
	tenantID := uint64(time.Now().UnixNano())

	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}
	seedPendingFile(t, chatSvc, buildRepo, c, "pages/home.liquid", "hi", GeneratedFileKindProposed)
	seedPendingFile(t, chatSvc, buildRepo, c, "pages/about.liquid", "there", GeneratedFileKindProposed)

	_, err = svc.ApplyDraft(ctx, tenantID, "tok", c.ID, "demo-theme")
	if err == nil {
		t.Fatal("expected an error from the partial failure")
	}
	if !strings.Contains(err.Error(), "pages/about.liquid") {
		t.Errorf("expected the error to name the failed path, got %v", err)
	}

	messages, err := chatSvc.ListMessagesForVerifiedChat(ctx, c.ID)
	if err != nil {
		t.Fatalf("ListMessagesForVerifiedChat failed: %v", err)
	}
	for _, m := range messages {
		if m.Role != chat.RoleAssistant {
			continue
		}
		if m.ApplyStatus != chat.ApplyStatusPending {
			t.Errorf("expected message %s to remain pending after a partial failure, got %q", m.ID, m.ApplyStatus)
		}
	}
}

// Item 10: DiscardDraft makes zero FlowPOS calls.
func TestDiscardDraft_MakesZeroFlowposCalls(t *testing.T) {
	fake := newFakeApplyServer()
	svc, chatSvc, buildRepo := newApplyTestService(t, fake)
	ctx := context.Background()
	tenantID := uint64(time.Now().UnixNano())

	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}
	seedPendingFile(t, chatSvc, buildRepo, c, "pages/home.liquid", "hi", GeneratedFileKindProposed)

	result, err := svc.DiscardDraft(ctx, tenantID, c.ID)
	if err != nil {
		t.Fatalf("DiscardDraft failed: %v", err)
	}
	if len(result.DiscardedPaths) != 1 || result.DiscardedTurns != 1 {
		t.Errorf("unexpected discard result: %+v", result)
	}
	if fake.callCount() != 0 {
		t.Fatalf("expected zero FlowPOS calls, got %d", fake.callCount())
	}

	messages, err := chatSvc.ListMessagesForVerifiedChat(ctx, c.ID)
	if err != nil {
		t.Fatalf("ListMessagesForVerifiedChat failed: %v", err)
	}
	for _, m := range messages {
		if m.Role == chat.RoleAssistant && m.ApplyStatus != chat.ApplyStatusDiscarded {
			t.Errorf("expected message %s discarded, got %q", m.ID, m.ApplyStatus)
		}
	}
}

// Item 12: revert to a turn inside the current draft makes no FlowPOS
// calls (see revertWithinDraft).
func TestRevertToMessage_WithinDraftMakesZeroFlowposCalls(t *testing.T) {
	fake := newFakeApplyServer()
	svc, chatSvc, buildRepo := newApplyTestService(t, fake)
	ctx := context.Background()
	tenantID := uint64(time.Now().UnixNano())

	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}
	target := seedPendingFile(t, chatSvc, buildRepo, c, "pages/home.liquid", "v1", GeneratedFileKindProposed)
	time.Sleep(1100 * time.Millisecond)
	seedPendingFile(t, chatSvc, buildRepo, c, "pages/home.liquid", "v2", GeneratedFileKindProposed)

	result, err := svc.RevertToMessage(ctx, tenantID, "tok", c.ID, target.ID)
	if err != nil {
		t.Fatalf("RevertToMessage failed: %v", err)
	}
	if fake.callCount() != 0 {
		t.Fatalf("expected zero FlowPOS calls for a within-draft revert, got %d", fake.callCount())
	}
	if len(result.RestoredFiles) != 1 || result.RestoredFiles[0] != "pages/home.liquid" {
		t.Errorf("expected pages/home.liquid reported as restored, got %+v", result)
	}

	// The later ('v2') turn must now be discarded, not pending — otherwise
	// DraftFiles would still return v2 as the "latest" pending content.
	draft, err := buildRepo.DraftFiles(ctx, c.ID)
	if err != nil {
		t.Fatalf("DraftFiles failed: %v", err)
	}
	if draft["pages/home.liquid"] != "v1" {
		t.Fatalf("expected the draft to reflect the reverted-to v1 content, got %q", draft["pages/home.liquid"])
	}
}

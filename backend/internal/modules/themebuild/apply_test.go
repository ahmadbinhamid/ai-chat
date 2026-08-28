package themebuild

import (
	"context"
	"encoding/base64"
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

func TestApplyDraft_CarriesForwardPageMetaAcrossTurns(t *testing.T) {
	fake := newFakeApplyServer()
	svc, chatSvc, buildRepo := newApplyTestService(t, fake)
	ctx := context.Background()
	tenantID := uint64(time.Now().UnixNano())

	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}

	// Turn 1: "add an About page" — the model registers it.
	msg1, err := chatSvc.RecordAssistantMessage(ctx, c, "turn 1", chat.MessageStatusCompleted, 0, 0, chat.ApplyStatusPending)
	if err != nil {
		t.Fatalf("RecordAssistantMessage (turn 1) failed: %v", err)
	}
	now := time.Now().UTC()
	if err := buildRepo.CreateFile(ctx, GeneratedFile{
		ID: uuid.NewString(), MessageID: msg1.ID, ChatID: c.ID, FilePath: "pages/about.liquid",
		Action: FileActionCreate, Kind: GeneratedFileKindProposed, Content: "<p>about v1</p>",
		PageMeta:  &themefs.PageMeta{Title: "About", Slug: "about", Type: "custom", Status: "draft"},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateFile (turn 1) failed: %v", err)
	}

	time.Sleep(1100 * time.Millisecond)

	// Turn 2: "make the heading bigger" — the page already exists, so the
	// model sends no PageRegistryEntry this time; pageMeta is nil.
	msg2, err := chatSvc.RecordAssistantMessage(ctx, c, "turn 2", chat.MessageStatusCompleted, 0, 0, chat.ApplyStatusPending)
	if err != nil {
		t.Fatalf("RecordAssistantMessage (turn 2) failed: %v", err)
	}
	now2 := time.Now().UTC()
	if err := buildRepo.CreateFile(ctx, GeneratedFile{
		ID: uuid.NewString(), MessageID: msg2.ID, ChatID: c.ID, FilePath: "pages/about.liquid",
		Action: FileActionUpdate, Kind: GeneratedFileKindProposed, Content: "<p>about v2</p>",
		PageMeta:  nil,
		CreatedAt: now2, UpdatedAt: now2,
	}); err != nil {
		t.Fatalf("CreateFile (turn 2) failed: %v", err)
	}

	if _, err := svc.ApplyDraft(ctx, tenantID, "tok", c.ID, "demo-theme"); err != nil {
		t.Fatalf("ApplyDraft failed: %v", err)
	}

	body := fake.bodies["pages/about.liquid"]
	if body == nil {
		t.Fatal("expected a write recorded for pages/about.liquid")
	}
	if body["content"] != "<p>about v2</p>" {
		t.Fatalf("expected turn 2's content to win, got %+v", body)
	}
	if body["title"] != "About" || body["slug"] != "about" {
		t.Fatalf("expected turn 1's page registration to survive to the write despite turn 2 carrying no PageMeta, got %+v", body)
	}
}

func TestPendingFilesToPlan_CollapseAcrossTurns(t *testing.T) {
	older := time.Now().UTC().Add(-time.Minute)
	newer := time.Now().UTC()
	metaV1 := &themefs.PageMeta{Title: "About", Slug: "about"}
	metaV2 := &themefs.PageMeta{Title: "About Us", Slug: "about-us"}

	tests := []struct {
		name         string
		files        []GeneratedFile
		wantPageMeta *themefs.PageMeta
		wantAction   FileAction
	}{
		{
			name: "newer turn's own PageMeta wins over older turn's",
			files: []GeneratedFile{
				{FilePath: "pages/about.liquid", Action: FileActionCreate, PageMeta: metaV1, CreatedAt: older},
				{FilePath: "pages/about.liquid", Action: FileActionUpdate, PageMeta: metaV2, CreatedAt: newer},
			},
			wantPageMeta: metaV2,
			wantAction:   FileActionCreate,
		},
		{
			name: "create then update collapses to create",
			files: []GeneratedFile{
				{FilePath: "pages/about.liquid", Action: FileActionCreate, CreatedAt: older},
				{FilePath: "pages/about.liquid", Action: FileActionUpdate, CreatedAt: newer},
			},
			wantPageMeta: nil,
			wantAction:   FileActionCreate,
		},
		{
			name: "update-only path stays update",
			files: []GeneratedFile{
				{FilePath: "pages/about.liquid", Action: FileActionUpdate, CreatedAt: older},
				{FilePath: "pages/about.liquid", Action: FileActionUpdate, CreatedAt: newer},
			},
			wantPageMeta: nil,
			wantAction:   FileActionUpdate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for i := range tt.files {
				tt.files[i].Kind = GeneratedFileKindProposed
			}
			plan := pendingFilesToPlan(tt.files)
			if len(plan.files) != 1 {
				t.Fatalf("expected exactly 1 collapsed file, got %d: %+v", len(plan.files), plan.files)
			}
			got := plan.files[0]
			if got.pageMeta != tt.wantPageMeta {
				t.Errorf("pageMeta = %+v, want %+v", got.pageMeta, tt.wantPageMeta)
			}
			if got.action != tt.wantAction {
				t.Errorf("action = %q, want %q", got.action, tt.wantAction)
			}
		})
	}
}

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

func TestApplyDraft_AppliedPathsExcludesLayoutRows(t *testing.T) {
	fake := newFakeApplyServer()
	svc, chatSvc, buildRepo := newApplyTestService(t, fake)
	ctx := context.Background()
	tenantID := uint64(time.Now().UnixNano())

	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}
	seedPendingFile(t, chatSvc, buildRepo, c, "pages/offers.liquid", "<p>offers</p>", GeneratedFileKindProposed)
	seedPendingFile(t, chatSvc, buildRepo, c, pathLayoutStart, `<link rel="stylesheet" href="/theme-assets/css/offers.css">`, GeneratedFileKindLayout)

	summary, err := svc.DraftSummary(ctx, c.ID)
	if err != nil {
		t.Fatalf("DraftSummary failed: %v", err)
	}
	if len(summary.FilePaths) != 1 {
		t.Fatalf("expected DraftSummary to report 1 file (layout excluded), got %+v", summary.FilePaths)
	}

	result, err := svc.ApplyDraft(ctx, tenantID, "tok", c.ID, "demo-theme")
	if err != nil {
		t.Fatalf("ApplyDraft failed: %v", err)
	}

	// The layout splice must still have actually been written...
	if fake.writes[pathLayoutStart] != 1 {
		t.Fatalf("expected the layout splice to still be written to FlowPOS, got writes=%+v", fake.writes)
	}
	// ...but AppliedPaths must agree with what DraftSummary told the
	// merchant was pending, not report an extra file they never saw listed.
	if len(result.AppliedPaths) != len(summary.FilePaths) {
		t.Fatalf("expected AppliedPaths (%+v) to match DraftSummary.FilePaths' count (%+v)", result.AppliedPaths, summary.FilePaths)
	}
	if len(result.AppliedPaths) != 1 || result.AppliedPaths[0] != "pages/offers.liquid" {
		t.Fatalf("expected AppliedPaths to contain only pages/offers.liquid, got %+v", result.AppliedPaths)
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

func TestSaveManualEdit_StagesAsPendingAndFoldsIntoDraft(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{"pages/home.liquid": "<h1>Original</h1>"})
	defer ts.Close()

	conn := openTestDB(t)
	chatSvc := chat.NewService(chat.NewRepository(conn))
	buildRepo := NewRepository(conn)
	svc := NewService(buildRepo, chatSvc, nil, themefs.NewStore(ts.URL), nil)
	ctx := context.Background()
	tenantID := uint64(time.Now().UnixNano())

	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}

	file, err := svc.SaveManualEdit(ctx, tenantID, "tok", c.ID, "pages/home.liquid", "<h1>Edited by merchant</h1>")
	if err != nil {
		t.Fatalf("SaveManualEdit failed: %v", err)
	}
	if file.Kind != GeneratedFileKindProposed || file.Action != FileActionUpdate {
		t.Errorf("expected a proposed/update row, got kind=%q action=%q", file.Kind, file.Action)
	}
	if file.PreviousContent == nil || *file.PreviousContent != "<h1>Original</h1>" {
		t.Errorf("expected previous_content to capture the pre-edit content, got %+v", file.PreviousContent)
	}

	draft, err := svc.DraftFiles(ctx, tenantID, "tok", c.ID)
	if err != nil {
		t.Fatalf("DraftFiles failed: %v", err)
	}
	if draft["pages/home.liquid"] != "<h1>Edited by merchant</h1>" {
		t.Fatalf("expected the manual edit to win in the effective draft, got %q", draft["pages/home.liquid"])
	}

	msg, err := chatSvc.GetMessage(ctx, c.ID, file.MessageID)
	if err != nil {
		t.Fatalf("GetMessage failed: %v", err)
	}
	if msg.Role != chat.RoleSystem {
		t.Errorf("expected the bookkeeping message to use RoleSystem, got %q", msg.Role)
	}
	if msg.ApplyStatus != chat.ApplyStatusPending {
		t.Errorf("expected the bookkeeping message to be pending (not yet applied), got %q", msg.ApplyStatus)
	}

	summary, err := svc.DraftSummary(ctx, c.ID)
	if err != nil {
		t.Fatalf("DraftSummary failed: %v", err)
	}
	if !summary.HasChanges {
		t.Error("expected DraftSummary.HasChanges to flip true — this is what shows the Apply/Discard bar")
	}
}

func TestSaveManualEdit_ImagePath_UsesAssetReadNotDraftFiles(t *testing.T) {
	originalBytes := []byte("fake-png-bytes")
	originalB64 := base64.StdEncoding.EncodeToString(originalBytes)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/store/themes/active/files" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"files": []themefs.FileTreeEntry{}}})
			return
		}
		if r.URL.Path == "/store/themes/active/files/images/hero.png" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"path": "images/hero.png", "content": originalB64, "encoding": "base64"},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	conn := openTestDB(t)
	chatSvc := chat.NewService(chat.NewRepository(conn))
	buildRepo := NewRepository(conn)
	svc := NewService(buildRepo, chatSvc, nil, themefs.NewStore(ts.URL), nil)
	ctx := context.Background()
	tenantID := uint64(time.Now().UnixNano())

	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}

	newB64 := base64.StdEncoding.EncodeToString([]byte("new-png-bytes"))
	file, err := svc.SaveManualEdit(ctx, tenantID, "tok", c.ID, "images/hero.png", newB64)
	if err != nil {
		t.Fatalf("SaveManualEdit failed: %v", err)
	}
	if file.Language != "IMAGE" {
		t.Errorf("expected Language IMAGE, got %q", file.Language)
	}
	if file.PreviousContent == nil || *file.PreviousContent != originalB64 {
		t.Errorf("expected previous_content to be the original base64, got %+v", file.PreviousContent)
	}
	if file.Content != newB64 {
		t.Errorf("expected content to be the new base64, got %q", file.Content)
	}
}

// TestSaveManualEdit_ImagePath_NotFound — an image path that doesn't exist
// as a real theme asset must be rejected the same way a missing text file is.
func TestSaveManualEdit_ImagePath_NotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	conn := openTestDB(t)
	chatSvc := chat.NewService(chat.NewRepository(conn))
	buildRepo := NewRepository(conn)
	svc := NewService(buildRepo, chatSvc, nil, themefs.NewStore(ts.URL), nil)
	ctx := context.Background()
	tenantID := uint64(time.Now().UnixNano())

	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}

	_, err = svc.SaveManualEdit(ctx, tenantID, "tok", c.ID, "images/does-not-exist.png", "Zm9v")
	if !errors.Is(err, ErrManualEditFileNotFound) {
		t.Fatalf("expected ErrManualEditFileNotFound, got %v", err)
	}
}

func TestSaveManualEdit_RejectsPathNotInDraft(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{"pages/home.liquid": "<h1>Original</h1>"})
	defer ts.Close()

	conn := openTestDB(t)
	chatSvc := chat.NewService(chat.NewRepository(conn))
	buildRepo := NewRepository(conn)
	svc := NewService(buildRepo, chatSvc, nil, themefs.NewStore(ts.URL), nil)
	ctx := context.Background()
	tenantID := uint64(time.Now().UnixNano())

	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}

	_, err = svc.SaveManualEdit(ctx, tenantID, "tok", c.ID, "pages/does-not-exist.liquid", "content")
	if !errors.Is(err, ErrManualEditFileNotFound) {
		t.Fatalf("expected ErrManualEditFileNotFound, got %v", err)
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

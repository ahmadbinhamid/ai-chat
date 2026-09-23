package themebuild

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/themefs"

	"github.com/google/uuid"
)

// Records assistant message with apply_status='pending' + staged GeneratedFile.
func seedPendingFile(t *testing.T, chatSvc *chat.Service, buildRepo *Repository, c chat.Chat, path, content string, kind GeneratedFileKind) chat.Message {
	t.Helper()
	msg, err := chatSvc.RecordAssistantMessage(context.Background(), c, "turn", chat.MessageStatusCompleted, 0, 0, chat.ApplyStatusPending)
	if err != nil {
		t.Fatalf("RecordAssistantMessage failed: %v", err)
	}
	now := time.Now().UTC()
	if err := buildRepo.CreateFile(context.Background(), GeneratedFile{
		ID: uuid.NewString(), MessageID: msg.ID, ChatID: c.ID, FilePath: path,
		Action: FileActionCreate, Kind: kind, Content: content, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateFile failed: %v", err)
	}
	return msg
}

func TestDraftFiles_LastWriteWinsAcrossThreeTurns(t *testing.T) {
	conn := openTestDB(t)
	chatSvc := chat.NewService(chat.NewRepository(conn))
	buildRepo := NewRepository(conn)
	ctx := context.Background()
	tenantID := uint64(time.Now().UnixNano())

	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}

	for _, content := range []string{"v1", "v2", "v3"} {
		seedPendingFile(t, chatSvc, buildRepo, c, "pages/home.liquid", content, GeneratedFileKindProposed)
	}

	draft, err := buildRepo.DraftFiles(ctx, c.ID)
	if err != nil {
		t.Fatalf("DraftFiles failed: %v", err)
	}
	if draft["pages/home.liquid"] != "v3" {
		t.Fatalf("expected the latest turn's content (v3) to win, got %q", draft["pages/home.liquid"])
	}
}

// Item 2: execReadThemeFile returns draft content, not FlowPOS content —
// the regression this whole feature hinges on (a model re-reading a file
func TestExecReadThemeFile_ReturnsDraftContentNotFlowposContent(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{"pages/home.liquid": "SAVED ON FLOWPOS"})
	defer ts.Close()

	svc := &Service{store: themefs.NewStore(ts.URL)}
	overlay := themefs.NewOverlayStore(svc.store, map[string]string{"pages/home.liquid": "DRAFT CONTENT"})

	input, err := json.Marshal(readThemeFileInput{Paths: []string{"pages/home.liquid"}})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	out, err := svc.execReadThemeFile(context.Background(), overlay, testStoreAuth(), input)
	if err != nil {
		t.Fatalf("execReadThemeFile failed: %v", err)
	}
	if !strings.Contains(out, "DRAFT CONTENT") {
		t.Fatalf("expected draft content in output, got %q", out)
	}
	if strings.Contains(out, "SAVED ON FLOWPOS") {
		t.Fatalf("expected NOT to see stale FlowPOS content, got %q", out)
	}
}

// Item 3: buildSnapshot sees a draft-created file in the merged tree —
// otherwise themecheck validates against the wrong file set and "repairs"
func TestBuildSnapshot_SeesDraftCreatedFileInMergedTree(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{}) // empty real theme
	defer ts.Close()

	svc := &Service{store: themefs.NewStore(ts.URL)}
	overlay := themefs.NewOverlayStore(svc.store, map[string]string{"pages/new.liquid": "content"})

	base, err := svc.buildSnapshotBase(context.Background(), overlay, testStoreAuth())
	if err != nil {
		t.Fatalf("buildSnapshotBase failed: %v", err)
	}
	snap := svc.buildSnapshot(context.Background(), overlay, testStoreAuth(), base, &ai.Result{})
	if !snap.Paths["pages/new.liquid"] {
		t.Fatalf("expected the draft-created file to appear in the snapshot's Paths, got %+v", snap.Paths)
	}
}

// TestBuildSnapshot_BaselineFetchFailureDoesNotFailGeneration: a store error
// fetching one proposed update's baseline content degrades that file to no
func TestBuildSnapshot_BaselineFetchFailureDoesNotFailGeneration(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/store/themes/active/files" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"files": []themefs.FileTreeEntry{}}, "status": true})
			return
		}
		reqPath := strings.TrimPrefix(r.URL.Path, "/store/themes/active/files/")
		if reqPath == "components/footer.liquid" {
			w.WriteHeader(http.StatusInternalServerError) // simulates a real store error, not a 404
			return
		}
		// Every other path (the four required files) — empty content, same
		// as a brand-new theme, matching newFakeThemeServer's own behavior
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	svc := &Service{store: themefs.NewStore(ts.URL)}
	result := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "components/footer.liquid", Action: "update", Content: "new content"},
	}}

	base, err := svc.buildSnapshotBase(context.Background(), svc.store, testStoreAuth())
	if err != nil {
		t.Fatalf("buildSnapshotBase failed: %v", err)
	}
	snap := svc.buildSnapshot(context.Background(), svc.store, testStoreAuth(), base, result)
	if _, ok := snap.Files["components/footer.liquid"]; ok {
		t.Errorf("expected no baseline entry for the file whose fetch failed, got %+v", snap.Files)
	}
}

// Item 4: two prompts in sequence — the second buildThemeContext call must
// see the first turn's own staged output (via the draft overlay's merged
func TestBuildThemeContext_SecondCallSeesFirstTurnsDraftOutput(t *testing.T) {
	conn := openTestDB(t)
	chatSvc := chat.NewService(chat.NewRepository(conn))
	buildRepo := NewRepository(conn)
	ts := newFakeThemeServer(t, map[string]string{}) // brand-new, empty theme
	defer ts.Close()

	svc := &Service{store: themefs.NewStore(ts.URL)}
	ctx := context.Background()
	tenantID := uint64(time.Now().UnixNano())

	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}

	// Turn 1's own staged output — as if doGenerate had just run.
	seedPendingFile(t, chatSvc, buildRepo, c, "pages/about.liquid", "turn 1 output", GeneratedFileKindProposed)

	// Turn 2 starts exactly like doGenerate does: load the draft, wrap it.
	draft, err := buildRepo.DraftFiles(ctx, c.ID)
	if err != nil {
		t.Fatalf("DraftFiles failed: %v", err)
	}
	overlay := themefs.NewOverlayStore(svc.store, draft)

	tc, err := svc.buildThemeContext(ctx, overlay, testStoreAuth(), "demo-theme")
	if err != nil {
		t.Fatalf("buildThemeContext failed: %v", err)
	}

	var sawPath bool
	var walk func([]themefs.FileTreeEntry)
	walk = func(entries []themefs.FileTreeEntry) {
		for _, e := range entries {
			if e.Path == "pages/about.liquid" {
				sawPath = true
			}
			walk(e.Children)
		}
	}
	walk(tc.FileTree)
	if !sawPath {
		t.Fatalf("expected the second turn's theme context to include the first turn's staged file, got tree %+v", tc.FileTree)
	}
}

// TestBuildThemeContext_ConcurrentCallsMatchSequentialShape confirms
// parallelizing buildThemeContext's four store round trips (see its own
func TestBuildThemeContext_ConcurrentCallsMatchSequentialShape(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{"pages.json": `[{"slug":"home"}]`, "defaults.json": `{"colors":{}}`})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}

	tc, err := svc.buildThemeContext(context.Background(), svc.store, testStoreAuth(), "demo-theme")
	if err != nil {
		t.Fatalf("buildThemeContext failed: %v", err)
	}
	if tc.ThemeSlug != "demo-theme" {
		t.Errorf("expected ThemeSlug to be passed through, got %q", tc.ThemeSlug)
	}
	if tc.PagesJSON != `[{"slug":"home"}]` {
		t.Errorf("expected pages.json content, got %q", tc.PagesJSON)
	}
	if tc.DefaultsJSON != `{"colors":{}}` {
		t.Errorf("expected defaults.json content, got %q", tc.DefaultsJSON)
	}
	if tc.Manifest == nil {
		t.Error("expected a non-nil Manifest even when the store has no components")
	}
}

// TestBuildThemeContext_FailsOnASingleReadError confirms the parallelized
// version still fails the whole call the same way a sequential one would —
func TestBuildThemeContext_FailsOnASingleReadError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/store/themes/active/files" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}

	if _, err := svc.buildThemeContext(context.Background(), svc.store, testStoreAuth(), "demo-theme"); err == nil {
		t.Error("expected an error when the file-listing call fails")
	}
}

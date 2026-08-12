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

// seedPendingFile records an assistant message with apply_status='pending'
// and one staged GeneratedFile row for it — the minimal shape a draft turn
// leaves behind (see doGenerate's staging path).
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

// Item 1: DraftFiles last-write-wins across three turns touching one path.
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
		// queued_at/created_at-style hazard (see DraftFiles' own doc
		// comment): without a gap, three inserts in the same wall-clock
		// second would tie on created_at and this test would just be
		// asserting arbitrary id-ordered output.
		time.Sleep(1100 * time.Millisecond)
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
// it just edited must never see the stale pre-edit version).
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
// non-problems (a render target that actually exists in the draft, just
// not on FlowPOS yet, would look like a missing-target error).
func TestBuildSnapshot_SeesDraftCreatedFileInMergedTree(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{}) // empty real theme
	defer ts.Close()

	svc := &Service{store: themefs.NewStore(ts.URL)}
	overlay := themefs.NewOverlayStore(svc.store, map[string]string{"pages/new.liquid": "content"})

	snap, err := svc.buildSnapshot(context.Background(), overlay, testStoreAuth(), &ai.Result{})
	if err != nil {
		t.Fatalf("buildSnapshot failed: %v", err)
	}
	if !snap.Paths["pages/new.liquid"] {
		t.Fatalf("expected the draft-created file to appear in the snapshot's Paths, got %+v", snap.Paths)
	}
}

// Item 4: two prompts in sequence — the second buildThemeContext call must
// see the first turn's own staged output (via the draft overlay's merged
// file tree), not just the last-applied theme.
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

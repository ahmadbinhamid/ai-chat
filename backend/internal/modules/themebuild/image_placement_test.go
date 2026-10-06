package themebuild

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/imageplacement"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/themecheck"
	"ai-chat/internal/themefs"

	"github.com/google/uuid"
)

var (
	testJPEG = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01}
	testPNG  = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	testSVG  = []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
)

var testUserID uint64 = 42

// heroUsing is a proposed component that uses imagePath, so a placement of it isn't sent back as unused.
func heroUsing(imagePath string) ai.GeneratedFile {
	return ai.GeneratedFile{Path: "components/hero.liquid", Action: "create",
		Content: `<img src="{{ '` + imagePath + `' | asset_url }}" alt="">`}
}

// recordingThemeServer is a fake FlowPOS theme API that logs every write in order, telling an
// upload (multipart, raw bytes) apart from a text write (JSON).
type recordingThemeServer struct {
	mu      sync.Mutex
	files   map[string]string
	ops     []string
	uploads map[string][]byte
	types   map[string]string
	// failUpload, if set, makes the upload to that path fail with a 500.
	failUpload string
}

func newRecordingThemeServer(t *testing.T, files map[string]string) (*recordingThemeServer, *httptest.Server) {
	t.Helper()
	f := &recordingThemeServer{files: files, uploads: make(map[string][]byte), types: make(map[string]string)}
	ts := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(ts.Close)
	return f, ts
}

func (f *recordingThemeServer) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/store/themes/active/files" {
		entries := make([]themefs.FileTreeEntry, 0, len(f.files))
		for p := range f.files {
			entries = append(entries, themefs.FileTreeEntry{Name: p, Path: p, Type: "file"})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"files": entries}, "status": true})
		return
	}
	reqPath := strings.TrimPrefix(r.URL.Path, "/store/themes/active/files/")
	switch r.Method {
	case http.MethodGet:
		f.ops = append(f.ops, "read "+reqPath)
		content, ok := f.files[reqPath]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"path": reqPath, "content": content, "encoding": "utf-8"}})
	case http.MethodPost:
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			if reqPath == f.failUpload {
				f.ops = append(f.ops, "failed upload "+reqPath)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			file, header, err := r.FormFile("file")
			if err != nil {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			data, _ := io.ReadAll(file)
			f.uploads[reqPath] = data
			f.types[reqPath] = header.Header.Get("Content-Type")
			f.ops = append(f.ops, "upload "+reqPath)
		} else {
			f.ops = append(f.ops, "write "+reqPath)
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		f.ops = append(f.ops, "delete "+reqPath)
		w.WriteHeader(http.StatusOK)
	}
}

// writes returns every non-read operation, in order.
func (f *recordingThemeServer) writes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, op := range f.ops {
		if !strings.HasPrefix(op, "read ") {
			out = append(out, op)
		}
	}
	return out
}

func (f *recordingThemeServer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.ops)
}

func baseThemeFiles() map[string]string {
	return map[string]string{
		"pages.json":                 `[]`,
		"defaults.json":              `{}`,
		"liquid/layout-start.liquid": "<html>",
		"liquid/layout-end.liquid":   "</html>",
		"images/logo.png":            "existing",
	}
}

type placementFixture struct {
	svc      *Service
	chatSvc  *chat.Service
	repo     *Repository
	server   *recordingThemeServer
	chat     chat.Chat
	tenantID uint64
}

func newPlacementFixture(t *testing.T) placementFixture {
	t.Helper()
	conn := openTestDB(t)
	chatSvc := chat.NewService(chat.NewRepository(conn))
	repo := NewRepository(conn)
	server, ts := newRecordingThemeServer(t, baseThemeFiles())
	svc := NewService(repo, chatSvc, nil, themefs.NewStore(ts.URL), nil)
	tenantID := uint64(time.Now().UnixNano())
	c, err := chatSvc.GetOrCreateChat(context.Background(), tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}
	return placementFixture{svc: svc, chatSvc: chatSvc, repo: repo, server: server, chat: c, tenantID: tenantID}
}

// sendImage records a user message carrying one image and returns it with its attachment ID.
func (f placementFixture) sendImage(t *testing.T, prompt string, data []byte, mediaType string) (chat.Message, string) {
	t.Helper()
	msg, err := f.chatSvc.RecordUserMessage(context.Background(), f.chat, &testUserID, "", "", prompt,
		[]chat.MessageImage{{Base64: base64.StdEncoding.EncodeToString(data), MediaType: mediaType}}, nil, nil, nil)
	if err != nil {
		t.Fatalf("RecordUserMessage failed: %v", err)
	}
	return msg, msg.Attachments[0].ID
}

func (f placementFixture) generate(t *testing.T, gen generator, prompt string, userMsg chat.Message) {
	t.Helper()
	f.svc.gen = gen
	ctx := context.Background()
	genID := uuid.NewString()
	if err := f.repo.StartGeneration(ctx, genID, f.chat.ID, f.tenantID); err != nil {
		t.Fatalf("StartGeneration failed: %v", err)
	}
	msgID := userMsg.ID
	in := GenerateInput{TenantID: f.tenantID, Token: "t", ThemeSlug: "test-theme", Prompt: prompt, UserMessageID: &msgID}
	if err := f.svc.doGenerate(ctx, in, f.chat, genID, nil); err != nil {
		t.Fatalf("doGenerate returned an error: %v", err)
	}
}

func (f placementFixture) pendingAttachmentRows(t *testing.T) []GeneratedFile {
	t.Helper()
	files, err := f.repo.PendingFiles(context.Background(), f.chat.ID)
	if err != nil {
		t.Fatalf("PendingFiles failed: %v", err)
	}
	var out []GeneratedFile
	for _, file := range files {
		if file.Kind == GeneratedFileKindAttachment {
			out = append(out, file)
		}
	}
	return out
}

func TestDoGenerate_PlacementIsStagedNotUploaded(t *testing.T) {
	f := newPlacementFixture(t)
	msg, attachmentID := f.sendImage(t, "use this image as the background of the hero section", testJPEG, "image/jpeg")

	f.generate(t, &fakeGenerator{results: []*ai.Result{{
		Summary:        "Added your photo to the theme.",
		Files:          []ai.GeneratedFile{heroUsing("images/hero.jpg")},
		UseAttachments: []ai.AttachmentPlacement{{Attachment: 1, Path: "images/hero.jpg"}},
	}}}, msg.Content, msg)

	if w := f.server.writes(); len(w) != 0 {
		t.Fatalf("staging must not reach FlowPOS, got writes %v", w)
	}
	rows := f.pendingAttachmentRows(t)
	if len(rows) != 1 {
		t.Fatalf("expected one staged attachment row, got %+v", rows)
	}
	row := rows[0]
	if row.FilePath != "images/hero.jpg" || row.Content != imageplacement.Reference(attachmentID) || row.Action != FileActionCreate {
		t.Errorf("staged row = %+v, want images/hero.jpg create referencing %s", row, attachmentID)
	}
	if row.Language != "" {
		t.Errorf("staged row language = %q, want empty (content is a reference, not image data)", row.Language)
	}

	summary, err := f.svc.DraftSummary(context.Background(), f.chat.ID)
	if err != nil || !summary.HasChanges || !slices.Contains(summary.FilePaths, "images/hero.jpg") {
		t.Errorf("DraftSummary = %+v, %v; want images/hero.jpg listed", summary, err)
	}
	changes, err := f.repo.FileChangesByChat(context.Background(), f.chat.ID)
	if err != nil {
		t.Fatalf("FileChangesByChat failed: %v", err)
	}
	if got := changes[row.MessageID]; !slices.ContainsFunc(got, func(c FileChange) bool { return c.FilePath == "images/hero.jpg" }) {
		t.Errorf("file history = %+v, want the placed image", got)
	}
	draft, err := f.repo.DraftFiles(context.Background(), f.chat.ID)
	if err != nil || draft["images/hero.jpg"] == "" {
		t.Errorf("draft overlay must read the placed image back as present, got %q (%v)", draft["images/hero.jpg"], err)
	}
}

func TestDoGenerate_RejectedPlacementIsRepaired(t *testing.T) {
	f := newPlacementFixture(t)
	msg, _ := f.sendImage(t, "put this photo on the about page", testJPEG, "image/jpeg")

	gen := &fakeGenerator{results: []*ai.Result{
		{Summary: "Placed it.", Files: []ai.GeneratedFile{heroUsing("images/about.jpg")},
			UseAttachments: []ai.AttachmentPlacement{{Attachment: 1, Path: "images/about.png"}}},
		{Summary: "Fixed.", UseAttachments: []ai.AttachmentPlacement{{Attachment: 1, Path: "images/about.jpg"}}},
	}}
	f.generate(t, gen, msg.Content, msg)

	if gen.calls != 2 {
		t.Fatalf("expected one repair round, got %d calls", gen.calls)
	}
	rows := f.pendingAttachmentRows(t)
	if len(rows) != 1 || rows[0].FilePath != "images/about.jpg" {
		t.Fatalf("expected only the repaired path staged, got %+v", rows)
	}
}

func TestDoGenerate_ReferenceOnlyRequestPlacesNothing(t *testing.T) {
	f := newPlacementFixture(t)
	msg, _ := f.sendImage(t, "make it look like this", testJPEG, "image/jpeg")
	gen := &capturingGenerator{visionSupported: true}

	f.generate(t, gen, msg.Content, msg)

	if rows := f.pendingAttachmentRows(t); len(rows) != 0 {
		t.Fatalf("a proposal without use_attachments must stage no image, got %+v", rows)
	}
	_, prompt, _ := gen.snapshot()
	for _, want := range []string{
		"Attached image 1: attached to this message",
		`("make it look like this") is a reference, not a placement`,
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestDoGenerate_EarlierPhotoResolvesToEarlierAttachment(t *testing.T) {
	f := newPlacementFixture(t)
	_, earlierID := f.sendImage(t, "here's our shop front", testJPEG, "image/jpeg")
	if _, err := f.chatSvc.RecordAssistantMessage(context.Background(), f.chat, "Nice photo!", chat.MessageStatusCompleted, 0, 0, chat.ApplyStatusNotApplicable); err != nil {
		t.Fatalf("RecordAssistantMessage failed: %v", err)
	}
	_, _ = f.sendImage(t, "and our logo", testPNG, "image/png")
	later, err := f.chatSvc.RecordUserMessage(context.Background(), f.chat, &testUserID, "", "", "use the photo I sent earlier on the about page", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("RecordUserMessage failed: %v", err)
	}

	f.generate(t, &fakeGenerator{results: []*ai.Result{{
		Summary:        "Placed your shop front photo.",
		Files:          []ai.GeneratedFile{heroUsing("images/shop-front.jpg")},
		UseAttachments: []ai.AttachmentPlacement{{Attachment: 1, Path: "images/shop-front.jpg"}},
	}}}, later.Content, later)

	rows := f.pendingAttachmentRows(t)
	if len(rows) != 1 || rows[0].Content != imageplacement.Reference(earlierID) {
		t.Fatalf("expected Attached image 1 to resolve to the earlier attachment %s, got %+v", earlierID, rows)
	}
}

func TestApplyDraft_UploadsPlacedImageBeforeWritingFiles(t *testing.T) {
	f := newPlacementFixture(t)
	_, attachmentID := f.sendImage(t, "use this as the hero background", testJPEG, "image/jpeg")
	// The page is staged first, so upload-before-write can't come from row order.
	seedPendingFile(t, f.chatSvc, f.repo, f.chat, "pages/home.liquid", `<img src="{{ 'images/hero.jpg' | asset_url }}">`, GeneratedFileKindProposed)
	seedPendingFile(t, f.chatSvc, f.repo, f.chat, "images/hero.jpg", imageplacement.Reference(attachmentID), GeneratedFileKindAttachment)

	result, err := f.svc.ApplyDraft(context.Background(), f.tenantID, "tok", f.chat.ID, "test-theme")
	if err != nil {
		t.Fatalf("ApplyDraft failed: %v", err)
	}
	if w := f.server.writes(); len(w) != 2 || w[0] != "upload images/hero.jpg" || w[1] != "write pages/home.liquid" {
		t.Fatalf("expected the upload before the page write, got %v", w)
	}
	if !bytes.Equal(f.server.uploads["images/hero.jpg"], testJPEG) || f.server.types["images/hero.jpg"] != "image/jpeg" {
		t.Errorf("uploaded %q as %q, want the attachment's exact bytes as image/jpeg", f.server.uploads["images/hero.jpg"], f.server.types["images/hero.jpg"])
	}
	if len(result.AppliedPaths) != 2 {
		t.Errorf("AppliedPaths = %v, want both paths", result.AppliedPaths)
	}
}

func TestDiscardAndRevert_UploadNothing(t *testing.T) {
	t.Run("discard", func(t *testing.T) {
		f := newPlacementFixture(t)
		_, attachmentID := f.sendImage(t, "use this", testJPEG, "image/jpeg")
		seedPendingFile(t, f.chatSvc, f.repo, f.chat, "images/hero.jpg", imageplacement.Reference(attachmentID), GeneratedFileKindAttachment)

		if _, err := f.svc.DiscardDraft(context.Background(), f.tenantID, f.chat.ID); err != nil {
			t.Fatalf("DiscardDraft failed: %v", err)
		}
		if n := f.server.callCount(); n != 0 {
			t.Fatalf("discard must make no FlowPOS calls, got %v", f.server.ops)
		}
	})

	t.Run("revert within draft", func(t *testing.T) {
		f := newPlacementFixture(t)
		_, attachmentID := f.sendImage(t, "use this", testJPEG, "image/jpeg")
		target := seedPendingFile(t, f.chatSvc, f.repo, f.chat, "pages/home.liquid", "<h1>hi</h1>", GeneratedFileKindProposed)
		seedPendingFile(t, f.chatSvc, f.repo, f.chat, "images/hero.jpg", imageplacement.Reference(attachmentID), GeneratedFileKindAttachment)

		if _, err := f.svc.RevertToMessage(context.Background(), f.tenantID, "tok", f.chat.ID, target.ID); err != nil {
			t.Fatalf("RevertToMessage failed: %v", err)
		}
		if n := f.server.callCount(); n != 0 {
			t.Fatalf("reverting a draft must make no FlowPOS calls, got %v", f.server.ops)
		}
	})

	t.Run("revert applied history", func(t *testing.T) {
		f := newPlacementFixture(t)
		_, attachmentID := f.sendImage(t, "use this", testJPEG, "image/jpeg")
		target := seedPendingFile(t, f.chatSvc, f.repo, f.chat, "pages/home.liquid", `<img src="{{ 'images/hero.jpg' | asset_url }}">`, GeneratedFileKindProposed)
		seedPendingFile(t, f.chatSvc, f.repo, f.chat, "images/hero.jpg", imageplacement.Reference(attachmentID), GeneratedFileKindAttachment)
		if _, err := f.svc.ApplyDraft(context.Background(), f.tenantID, "tok", f.chat.ID, "test-theme"); err != nil {
			t.Fatalf("ApplyDraft failed: %v", err)
		}
		applied := len(f.server.writes())

		result, err := f.svc.RevertToMessage(context.Background(), f.tenantID, "tok", f.chat.ID, target.ID)
		if err != nil {
			t.Fatalf("RevertToMessage failed: %v", err)
		}
		after := f.server.writes()[applied:]
		deleted := false
		for _, op := range after {
			if strings.HasPrefix(op, "upload ") {
				t.Fatalf("reverting must upload nothing, got %v", after)
			}
			deleted = deleted || op == "delete images/hero.jpg"
		}
		if !deleted || !slices.Contains(result.DeletedFiles, "images/hero.jpg") {
			t.Errorf("reverting past a placement must delete it, got ops %v, DeletedFiles %v", after, result.DeletedFiles)
		}
	})
}

func TestReadPreviewAssetBytes_ServesStagedImageFromAttachment(t *testing.T) {
	f := newPlacementFixture(t)
	ctx := context.Background()
	auth := themefs.RequestAuth{Token: "t", TenantID: f.tenantID}
	_, attachmentID := f.sendImage(t, "use this", testJPEG, "image/jpeg")
	seedPendingFile(t, f.chatSvc, f.repo, f.chat, "images/hero.jpg", imageplacement.Reference(attachmentID), GeneratedFileKindAttachment)

	data, err := f.svc.ReadPreviewAssetBytes(ctx, auth, "images/hero.jpg")
	if err != nil {
		t.Fatalf("ReadPreviewAssetBytes failed: %v", err)
	}
	if !bytes.Equal(data, testJPEG) {
		t.Errorf("served %q, want the attachment's bytes", data)
	}
	if n := f.server.callCount(); n != 0 {
		t.Errorf("a staged image must not be fetched from FlowPOS, got %v", f.server.ops)
	}

	if _, err := f.svc.DiscardDraft(ctx, f.tenantID, f.chat.ID); err != nil {
		t.Fatalf("DiscardDraft failed: %v", err)
	}
	data, err = f.svc.ReadPreviewAssetBytes(ctx, auth, "images/hero.jpg")
	if err != nil || data != nil {
		t.Errorf("after discard, expected FlowPOS's 404 (nil bytes), got %q, %v", data, err)
	}
	if n := f.server.callCount(); n != 1 {
		t.Errorf("after discard the path must come from FlowPOS, got %v", f.server.ops)
	}
}

func TestImageCatalog_Findings(t *testing.T) {
	images := map[string][]byte{"a-jpeg": testJPEG, "a-svg": testSVG, "a-text": []byte("not an image"),
		"a-big": append(append([]byte(nil), testJPEG...), make([]byte, imageplacement.DefaultMaxBytes)...)}
	messages := []chat.Message{{ID: "m1", Role: chat.RoleUser, Attachments: []chat.MessageAttachment{
		{ID: "a-jpeg", Kind: chat.AttachmentKindImage}, {ID: "a-svg", Kind: chat.AttachmentKindImage},
		{ID: "a-text", Kind: chat.AttachmentKindImage}, {ID: "a-big", Kind: chat.AttachmentKindImage},
	}}}
	load := func(_ context.Context, id string) ([]byte, error) {
		if data, ok := images[id]; ok {
			return data, nil
		}
		return nil, errors.New("not found")
	}
	snap := themecheck.Snapshot{Paths: map[string]bool{"images/logo.jpg": true, "images/staged.jpg": true}}
	draft := map[string]string{"images/staged.jpg": imageplacement.Reference("a-jpeg")}

	tests := []struct {
		name    string
		place   ai.AttachmentPlacement
		wantErr string // "" means accepted
	}{
		{"valid", ai.AttachmentPlacement{Attachment: 1, Path: "images/hero.jpg"}, ""},
		{"same image already staged here", ai.AttachmentPlacement{Attachment: 1, Path: "images/staged.jpg"}, ""},
		{"jpeg named png", ai.AttachmentPlacement{Attachment: 1, Path: "images/hero.png"}, "must end in .jpg"},
		{"svg path", ai.AttachmentPlacement{Attachment: 2, Path: "images/logo.svg"}, "SVG can't be placed"},
		{"svg bytes", ai.AttachmentPlacement{Attachment: 2, Path: "images/logo.png"}, "not a PNG, JPEG or WebP"},
		{"non-image", ai.AttachmentPlacement{Attachment: 3, Path: "images/notes.png"}, "not a PNG, JPEG or WebP"},
		{"oversized", ai.AttachmentPlacement{Attachment: 4, Path: "images/big.jpg"}, "over the 2 MB limit"},
		{"existing path", ai.AttachmentPlacement{Attachment: 1, Path: "images/logo.jpg"}, "already exists"},
		{"other image staged here", ai.AttachmentPlacement{Attachment: 4, Path: "images/staged.jpg"}, "over the 2 MB limit"},
		{"unknown number", ai.AttachmentPlacement{Attachment: 9, Path: "images/hero.jpg"}, "no attached image 9"},
		{"zero", ai.AttachmentPlacement{Attachment: 0, Path: "images/hero.jpg"}, "no attached image 0"},
		{"unsafe path", ai.AttachmentPlacement{Attachment: 1, Path: "images/../../etc/x.jpg"}, ".."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalog := newImageCatalog(messages, "m1", draft, load)
			findings := catalog.findings(context.Background(), []ai.AttachmentPlacement{tt.place}, snap, []string{tt.place.Path})
			if tt.wantErr == "" {
				if len(findings) != 0 {
					t.Fatalf("expected accepted, got %+v", findings)
				}
				return
			}
			if len(findings) != 1 || findings[0].Severity != themecheck.SeverityError || findings[0].Rule != ruleIDUseAttachments ||
				!strings.Contains(findings[0].Message, tt.wantErr) {
				t.Fatalf("findings = %+v, want one blocking %s finding containing %q", findings, ruleIDUseAttachments, tt.wantErr)
			}
		})
	}
}

func TestImageCatalog_NumbersAcrossTurnsAndSkipsNonImages(t *testing.T) {
	messages := []chat.Message{
		{ID: "m1", Role: chat.RoleUser, Attachments: []chat.MessageAttachment{{ID: "a1", Kind: chat.AttachmentKindImage, Filename: "shop.jpg", MediaType: "image/jpeg"}}},
		{ID: "r1", Role: chat.RoleAssistant},
		{ID: "m2", Role: chat.RoleUser, Attachments: []chat.MessageAttachment{{ID: "h1", Kind: chat.AttachmentKindHTML}}},
		{ID: "m3", Role: chat.RoleUser, Attachments: []chat.MessageAttachment{
			{ID: "a2", Kind: chat.AttachmentKindImage, Filename: "logo.png", MediaType: "image/png"},
			{ID: "a3", Kind: chat.AttachmentKindImage, Filename: "team.webp", MediaType: "image/webp"},
		}},
	}
	catalog := newImageCatalog(messages, "m3", nil, nil)

	for n, wantID := range map[int]string{1: "a1", 2: "a2", 3: "a3"} {
		if img, ok := catalog.byNumber(n); !ok || img.ID != wantID {
			t.Errorf("Attached image %d = %+v, want %s", n, img, wantID)
		}
	}
	block := catalog.promptBlock()
	for _, want := range []string{
		"Attached image 1: sent 2 messages ago — shop.jpg — not placed yet",
		"Attached image 2: attached to this message — logo.png — not placed yet",
		"Attached image 3: attached to this message (the newest image) — team.webp — not placed yet",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("prompt block missing %q:\n%s", want, block)
		}
	}
	if (*imageCatalog)(nil).promptBlock() != "" || newImageCatalog(nil, "", nil, nil).promptBlock() != "" {
		t.Error("a chat with no images must add nothing to the prompt")
	}
}

func TestImageCatalog_LabelsTimingSizeAndPlacement(t *testing.T) {
	messages := []chat.Message{
		{ID: "m1", Role: chat.RoleUser, Attachments: []chat.MessageAttachment{{ID: "live", Kind: chat.AttachmentKindImage, Filename: "image-0.jpg"}}},
		{ID: "r1", Role: chat.RoleAssistant},
		{ID: "m2", Role: chat.RoleUser, Attachments: []chat.MessageAttachment{{ID: "staged", Kind: chat.AttachmentKindImage, Filename: "image-0.png"}}},
		{ID: "r2", Role: chat.RoleAssistant},
		{ID: "m3", Role: chat.RoleUser, Attachments: []chat.MessageAttachment{{ID: "fresh", Kind: chat.AttachmentKindImage, Filename: "image-0.webp"}}},
		{ID: "r3", Role: chat.RoleAssistant},
		{ID: "m4", Role: chat.RoleUser},
	}
	catalog := newImageCatalog(messages, "m4", nil, nil)
	catalog.describe(
		map[string][]byte{"live": jpegOfSize(t, 1600, 1067)},
		map[string]PlacedImage{
			"live":   {Path: "images/hero-main.jpg", Live: true},
			"staged": {Path: "images/about.png"},
		},
	)

	block := catalog.promptBlock()
	for _, want := range []string{
		`Images attached to this message are what "this image" and "it" mean.`,
		"a screenshot or graphic is as valid as a photo. Never refuse or second-guess it.",
		"Attached image 1: sent 3 messages ago — image-0.jpg — 1600×1067 px — already placed at images/hero-main.jpg (live in the theme)",
		"Attached image 2: sent 2 messages ago — image-0.png — already placed at images/about.png (staged — goes live when the merchant applies)",
		"Attached image 3: sent 1 message ago (the newest image) — image-0.webp — not placed yet",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("prompt block missing %q:\n%s", want, block)
		}
	}
}

func jpegOfSize(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

func TestImageCatalog_UnusedPlacementGoesToRepair(t *testing.T) {
	messages := []chat.Message{{ID: "m1", Role: chat.RoleUser, Attachments: []chat.MessageAttachment{{ID: "a1", Kind: chat.AttachmentKindImage}}}}
	load := func(context.Context, string) ([]byte, error) { return testJPEG, nil }
	place := []ai.AttachmentPlacement{{Attachment: 1, Path: "images/x.jpg"}}
	want := "You placed images/x.jpg but no file uses it. Either reference it where the merchant asked, or remove the " +
		"placement and tell them images are only added to the theme when a page uses them."

	tests := []struct {
		name      string
		draft     map[string]string
		proposal  []string
		wantError bool
	}{
		{"nothing uses it", nil, []string{"<h1>Hi</h1>"}, true},
		{"only its own staged reference mentions it", map[string]string{"images/x.jpg": imageplacement.Reference("a1")}, nil, true},
		{"used by a proposed file", nil, []string{`<img src="{{ 'images/x.jpg' | asset_url }}">`}, false},
		{"used by CSS in the proposal", nil, []string{`.hero { background: url("{{ 'images/x.jpg' | asset_url }}"); }`}, false},
		{"used by a draft file", map[string]string{"components/hero.liquid": `{{ 'images/x.jpg' | asset_url }}`}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalog := newImageCatalog(messages, "m1", tt.draft, load)
			snap := themecheck.Snapshot{Paths: map[string]bool{}}
			if tt.draft["images/x.jpg"] != "" {
				snap.Paths["images/x.jpg"] = true
			}
			findings := catalog.findings(context.Background(), place, snap, tt.proposal)
			if !tt.wantError {
				if len(findings) != 0 {
					t.Fatalf("expected a used placement to pass, got %+v", findings)
				}
				return
			}
			if len(findings) != 1 || findings[0].Message != want || findings[0].Severity != themecheck.SeverityError {
				t.Fatalf("findings = %+v, want one blocking finding %q", findings, want)
			}
		})
	}
}

func TestDoGenerate_UnusedPlacementRepairedWithHonestSummary(t *testing.T) {
	f := newPlacementFixture(t)
	msg, _ := f.sendImage(t, "just add this photo to my images folder", testJPEG, "image/jpeg")

	gen := &fakeGenerator{results: []*ai.Result{
		{Summary: "Saved your photo to the theme's images folder.",
			UseAttachments: []ai.AttachmentPlacement{{Attachment: 1, Path: "images/photo.jpg"}}},
		{Summary: "Images are only added to your theme when a page uses them — tell me where this photo should go."},
	}}
	f.generate(t, gen, msg.Content, msg)

	if gen.calls != 2 {
		t.Fatalf("expected the unused placement to trigger one repair round, got %d calls", gen.calls)
	}
	if rows := f.pendingAttachmentRows(t); len(rows) != 0 {
		t.Fatalf("a placement the repair removed must not be staged, got %+v", rows)
	}
	messages, err := f.chatSvc.ListMessagesForVerifiedChat(context.Background(), f.chat.ID)
	if err != nil {
		t.Fatalf("ListMessagesForVerifiedChat failed: %v", err)
	}
	reply := messages[len(messages)-1]
	if reply.Role != chat.RoleAssistant || !strings.Contains(reply.Content, "only added to your theme when a page uses them") {
		t.Fatalf("the merchant must hear why nothing was saved, got %q", reply.Content)
	}
	if strings.Contains(reply.Content, "Saved your photo") {
		t.Fatalf("the original fake-success summary must not survive, got %q", reply.Content)
	}
}

func TestMergeRepairIntoProposal_KeepsPlacementsUnlessRepairResubmits(t *testing.T) {
	original := &ai.Result{UseAttachments: []ai.AttachmentPlacement{{Attachment: 1, Path: "images/a.jpg"}}}

	kept := mergeRepairIntoProposal(original, &ai.Result{Files: []ai.GeneratedFile{{Path: "pages/x.liquid", Action: "update"}}})
	if len(kept.UseAttachments) != 1 || kept.UseAttachments[0].Path != "images/a.jpg" {
		t.Errorf("a repair that doesn't touch placements must keep them, got %+v", kept.UseAttachments)
	}
	replaced := mergeRepairIntoProposal(original, &ai.Result{UseAttachments: []ai.AttachmentPlacement{{Attachment: 1, Path: "images/b.jpg"}}})
	if len(replaced.UseAttachments) != 1 || replaced.UseAttachments[0].Path != "images/b.jpg" {
		t.Errorf("a resubmitted placement list must replace the original, got %+v", replaced.UseAttachments)
	}
}

func TestPendingFilesToPlan_AttachmentRowCarriesItsReference(t *testing.T) {
	plan := pendingFilesToPlan([]GeneratedFile{
		{FilePath: "images/hero.jpg", Action: FileActionCreate, Kind: GeneratedFileKindAttachment, Content: imageplacement.Reference("att-1")},
		{FilePath: "pages/home.liquid", Action: FileActionUpdate, Kind: GeneratedFileKindProposed, Content: "x"},
	})
	if len(plan.files) != 2 || plan.files[0].attachmentID != "att-1" || plan.files[1].attachmentID != "" {
		t.Fatalf("plan = %+v, want the image row to carry att-1 and the page none", plan.files)
	}
}

func TestValidateProposal_BrandModeRejectsPlacement(t *testing.T) {
	r := &ai.Result{UseAttachments: []ai.AttachmentPlacement{{Attachment: 1, Path: "images/a.jpg"}}}
	if err := validateProposal(r, ai.GenerationModeBrand); err == nil {
		t.Fatal("brand mode must not place images")
	}
}

func (f placementFixture) messagesWithStatus(t *testing.T, status chat.ApplyStatus) int {
	t.Helper()
	messages, err := f.chatSvc.ListMessagesForVerifiedChat(context.Background(), f.chat.ID)
	if err != nil {
		t.Fatalf("ListMessagesForVerifiedChat failed: %v", err)
	}
	n := 0
	for _, m := range messages {
		if m.Role == chat.RoleAssistant && m.ApplyStatus == status {
			n++
		}
	}
	return n
}

func TestApplyDraft_UploadsOnlyReferencedImages(t *testing.T) {
	f := newPlacementFixture(t)
	ctx := context.Background()
	_, replacedID := f.sendImage(t, "use this as the hero", testJPEG, "image/jpeg")
	_, usedID := f.sendImage(t, "actually use this one", testJPEG, "image/jpeg")
	seedPendingFile(t, f.chatSvc, f.repo, f.chat, "images/first-try.jpg", imageplacement.Reference(replacedID), GeneratedFileKindAttachment)
	seedPendingFile(t, f.chatSvc, f.repo, f.chat, "images/home-hero.jpg", imageplacement.Reference(usedID), GeneratedFileKindAttachment)
	seedPendingFile(t, f.chatSvc, f.repo, f.chat, "components/hero.liquid", `<img src="{{ 'images/home-hero.jpg' | asset_url }}">`, GeneratedFileKindProposed)

	result, err := f.svc.ApplyDraft(ctx, f.tenantID, "tok", f.chat.ID, "test-theme")
	if err != nil {
		t.Fatalf("ApplyDraft failed: %v", err)
	}
	if w := f.server.writes(); len(w) != 2 || w[0] != "upload images/home-hero.jpg" || w[1] != "write components/hero.liquid" {
		t.Fatalf("expected only the referenced image uploaded, got %v", w)
	}
	if slices.Contains(result.AppliedPaths, "images/first-try.jpg") {
		t.Errorf("AppliedPaths %v must not include the skipped image", result.AppliedPaths)
	}
	if pending := f.messagesWithStatus(t, chat.ApplyStatusPending); pending != 0 {
		t.Errorf("expected nothing left pending, got %d pending turns", pending)
	}
	files, err := f.repo.ListFilesByChat(ctx, f.chat.ID)
	if err != nil {
		t.Fatalf("ListFilesByChat failed: %v", err)
	}
	for _, file := range files {
		if file.FilePath == "images/first-try.jpg" && file.Kind != GeneratedFileKindDroppedAttachment {
			t.Errorf("skipped image row kind = %q, want %q", file.Kind, GeneratedFileKindDroppedAttachment)
		}
	}
	applied, err := f.repo.ListAppliedFilesByChat(ctx, f.chat.ID)
	if err != nil {
		t.Fatalf("ListAppliedFilesByChat failed: %v", err)
	}
	for _, file := range applied {
		if file.FilePath == "images/first-try.jpg" {
			t.Error("a dropped image must not count as applied, or revert would act on it")
		}
	}
}

func TestApplyDraft_UploadsImageReferencedOnlyFromCSS(t *testing.T) {
	f := newPlacementFixture(t)
	_, attachmentID := f.sendImage(t, "use this as the hero background", testJPEG, "image/jpeg")
	seedPendingFile(t, f.chatSvc, f.repo, f.chat, "images/hero-bg.jpg", imageplacement.Reference(attachmentID), GeneratedFileKindAttachment)
	seedPendingFile(t, f.chatSvc, f.repo, f.chat, "components/css/hero.css", `.hero { background-image: url("{{ 'images/hero-bg.jpg' | asset_url }}"); }`, GeneratedFileKindProposed)

	if _, err := f.svc.ApplyDraft(context.Background(), f.tenantID, "tok", f.chat.ID, "test-theme"); err != nil {
		t.Fatalf("ApplyDraft failed: %v", err)
	}
	if w := f.server.writes(); len(w) != 2 || w[0] != "upload images/hero-bg.jpg" {
		t.Fatalf("expected the CSS-referenced image uploaded first, got %v", w)
	}
}

func TestApplyDraft_FailedSecondUploadWritesNothingAndKeepsDraft(t *testing.T) {
	f := newPlacementFixture(t)
	_, firstID := f.sendImage(t, "use this", testJPEG, "image/jpeg")
	_, secondID := f.sendImage(t, "and this", testJPEG, "image/jpeg")
	seedPendingFile(t, f.chatSvc, f.repo, f.chat, "images/one.jpg", imageplacement.Reference(firstID), GeneratedFileKindAttachment)
	seedPendingFile(t, f.chatSvc, f.repo, f.chat, "images/two.jpg", imageplacement.Reference(secondID), GeneratedFileKindAttachment)
	seedPendingFile(t, f.chatSvc, f.repo, f.chat, "pages/home.liquid",
		`<img src="{{ 'images/one.jpg' | asset_url }}"><img src="{{ 'images/two.jpg' | asset_url }}">`, GeneratedFileKindProposed)
	f.server.failUpload = "images/two.jpg"

	_, err := f.svc.ApplyDraft(context.Background(), f.tenantID, "tok", f.chat.ID, "test-theme")
	if !errors.Is(err, ErrImageUploadFailed) {
		t.Fatalf("expected ErrImageUploadFailed, got %v", err)
	}
	want := []string{"upload images/one.jpg", "failed upload images/two.jpg", "delete images/one.jpg"}
	if w := f.server.writes(); !slices.Equal(w, want) {
		t.Fatalf("expected the first upload rolled back and no file written, got %v", w)
	}
	if pending := f.messagesWithStatus(t, chat.ApplyStatusPending); pending != 3 {
		t.Errorf("expected all 3 turns still pending for a retry, got %d", pending)
	}
	if msg := ErrImageUploadFailed.Error(); !strings.Contains(msg, "nothing was applied") || !strings.Contains(msg, "try Apply again") {
		t.Errorf("merchant message %q must say nothing changed and that a retry is possible", msg)
	}
}

func TestDoGenerate_OversizedImageGoesToRepair(t *testing.T) {
	f := newPlacementFixture(t)
	f.svc.SetPlacedImageMaxBytes(64)
	big := append(append([]byte(nil), testJPEG...), make([]byte, 100)...)
	_, _ = f.sendImage(t, "here's a big one", big, "image/jpeg")
	msg, smallID := f.sendImage(t, "use this image in the hero", testJPEG, "image/jpeg")

	gen := &fakeGenerator{results: []*ai.Result{
		{Summary: "Placed it.", Files: []ai.GeneratedFile{heroUsing("images/hero.jpg")},
			UseAttachments: []ai.AttachmentPlacement{{Attachment: 1, Path: "images/hero.jpg"}}},
		{Summary: "Placed it.", UseAttachments: []ai.AttachmentPlacement{{Attachment: 2, Path: "images/hero.jpg"}}},
	}}
	f.generate(t, gen, msg.Content, msg)

	if gen.calls != 2 {
		t.Fatalf("expected the oversized placement to trigger one repair round, got %d calls", gen.calls)
	}
	rows := f.pendingAttachmentRows(t)
	if len(rows) != 1 || rows[0].Content != imageplacement.Reference(smallID) {
		t.Fatalf("expected only the in-limit image staged, got %+v", rows)
	}
	events, err := f.repo.GetEventsSince(context.Background(), f.chat.ID, 0)
	if err != nil {
		t.Fatalf("GetEventsSince failed: %v", err)
	}
	found := false
	for _, ev := range events {
		found = found || (ev.Type == EventTypeCheckFailed && strings.Contains(string(ev.Payload), ruleIDUseAttachments) &&
			strings.Contains(string(ev.Payload), "limit for theme images"))
	}
	if !found {
		t.Error("expected a check_failed event naming the size limit, so the model is told why")
	}
}

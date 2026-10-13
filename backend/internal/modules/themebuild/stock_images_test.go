package themebuild

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ai-chat/internal/ai"
	"ai-chat/internal/imageplacement"
	"ai-chat/internal/stockimages"
	"ai-chat/internal/themecheck"
	"ai-chat/internal/themefs"

	"github.com/google/uuid"
)

type fakeStock struct {
	photos    []stockimages.Photo
	err       error
	searches  int
	tenant    uint64
	downloads int
	data      []byte
	dlErr     error
}

func (f *fakeStock) Search(_ context.Context, tenantID uint64, _ stockimages.Input) ([]stockimages.Photo, error) {
	f.searches++
	f.tenant = tenantID
	return f.photos, f.err
}

func (f *fakeStock) Download(_ context.Context, _ stockimages.Photo, maxBytes int) ([]byte, error) {
	f.downloads++
	if f.dlErr != nil {
		return nil, f.dlErr
	}
	if len(f.data) > maxBytes {
		return nil, stockimages.ErrTooLarge
	}
	return f.data, nil
}

func TestStockImagesFor(t *testing.T) {
	tests := []struct {
		name  string
		stock bool
		in    GenerateInput
		want  bool
	}{
		{"redesign", true, GenerateInput{redesign: true}, true},
		{"new page by prompt", true, GenerateInput{Prompt: "create an about us page"}, true},
		{"pages mode", true, GenerateInput{Mode: ai.GenerationModePages, Prompt: "x"}, true},
		{"ordinary edit", true, GenerateInput{Prompt: "make the button red"}, false},
		{"redesign in brand mode", true, GenerateInput{Mode: ai.GenerationModeBrand, redesign: true}, false},
		{"redesign in copy mode", true, GenerateInput{Mode: ai.GenerationModeCopy, redesign: true}, false},
		{"no provider configured", false, GenerateInput{redesign: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{}
			if tt.stock {
				s.SetStockImages(&fakeStock{})
			}
			if got := s.stockImagesFor(tt.in); got != tt.want {
				t.Errorf("stockImagesFor = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestToolExec_SearchStockImages(t *testing.T) {
	beans := stockimages.Photo{ID: 7, Description: "coffee, beans", Width: 1280, Height: 853, Photographer: "Ana"}
	tests := []struct {
		name         string
		offered      bool
		input        string
		stock        *fakeStock
		wantErr      string
		wantSearches int
	}{
		{"returns and records photos", true, `{"query":"coffee beans","count":2}`, &fakeStock{photos: []stockimages.Photo{beans}}, "", 1},
		{"no results", true, `{"query":"coffee beans"}`, &fakeStock{}, "", 1},
		{"not offered this turn", false, `{"query":"coffee beans"}`, &fakeStock{}, "unknown tool", 0},
		{"invalid input never reaches the provider", true, `{"query":"https://evil.example/a.jpg"}`, &fakeStock{}, "not a URL", 0},
		{"provider failure", true, `{"query":"coffee"}`, &fakeStock{err: stockimages.ErrRateLimited}, "continue with the theme's own images", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{}
			s.SetStockImages(tt.stock)
			var found *stockPhotos
			if tt.offered {
				found = newStockPhotos()
			}
			exec := s.buildToolExecutorWithPreload(nil, themefs.RequestAuth{}, toolOptions{tenantID: 77, stockPhotos: found})
			out, err := exec(context.Background(), ai.ToolNameSearchStockImages, json.RawMessage(tt.input))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if tt.stock.searches != tt.wantSearches {
				t.Fatalf("provider searches = %d, want %d", tt.stock.searches, tt.wantSearches)
			}
			if tt.wantSearches == 0 || err != nil {
				return
			}
			if tt.stock.tenant != 77 {
				t.Errorf("searched as tenant %d, want 77", tt.stock.tenant)
			}
			if strings.Contains(out, "http") {
				t.Errorf("the model must never see a photo URL: %s", out)
			}
			for _, p := range tt.stock.photos {
				if _, ok := found.get(p.ID); !ok {
					t.Errorf("photo %d was returned but not recorded for this turn", p.ID)
				}
			}
		})
	}
}

// stockCatalog is an image catalog for a turn whose search returned photo 7, downloading data through stock.
func stockCatalog(stock *fakeStock, saved *[][]byte) *imageCatalog {
	c := newImageCatalog(nil, "", nil, nil)
	found := newStockPhotos()
	found.record([]stockimages.Photo{{ID: 7, Description: "coffee"}})
	c.stock = found
	c.maxBytes = 1000
	c.downloadStock = func(ctx context.Context, p stockimages.Photo) ([]byte, error) {
		return stock.Download(ctx, p, c.maxBytes)
	}
	c.saveStock = func(_ context.Context, filename, mediaType string, data []byte) (string, error) {
		*saved = append(*saved, data)
		return "att-" + filename + "-" + mediaType, nil
	}
	return c
}

func TestImageCatalog_StockPlacements(t *testing.T) {
	uses := `<img src="{{ 'images/hero.jpg' | asset_url }}" alt="Beans">`
	tests := []struct {
		name      string
		placement ai.AttachmentPlacement
		stock     *fakeStock
		taken     bool
		used      string
		want      string
	}{
		{"a returned photo, used", ai.AttachmentPlacement{StockImage: 7, Path: "images/hero.jpg"}, &fakeStock{data: testJPEG}, false, uses, ""},
		{"an id this turn never returned", ai.AttachmentPlacement{StockImage: 99, Path: "images/hero.jpg"}, &fakeStock{data: testJPEG}, false, uses, "wasn't returned by search_stock_images"},
		{"both sources set", ai.AttachmentPlacement{Attachment: 1, StockImage: 7, Path: "images/hero.jpg"}, &fakeStock{data: testJPEG}, false, uses, "set only one"},
		{"over the size limit", ai.AttachmentPlacement{StockImage: 7, Path: "images/hero.jpg"}, &fakeStock{data: bytes.Repeat([]byte{0xFF}, 2000)}, false, uses, "choose another photo"},
		{"not an image", ai.AttachmentPlacement{StockImage: 7, Path: "images/hero.jpg"}, &fakeStock{data: []byte("<html>")}, false, uses, "not a PNG, JPEG or WebP"},
		{"wrong extension", ai.AttachmentPlacement{StockImage: 7, Path: "images/hero.png"}, &fakeStock{data: testJPEG}, false, `{{ 'images/hero.png' | asset_url }}`, "must end in .jpg"},
		{"outside images/", ai.AttachmentPlacement{StockImage: 7, Path: "assets/hero.jpg"}, &fakeStock{data: testJPEG}, false, `{{ 'assets/hero.jpg' | asset_url }}`, "under images/"},
		{"path already taken", ai.AttachmentPlacement{StockImage: 7, Path: "images/hero.jpg"}, &fakeStock{data: testJPEG}, true, uses, "already exists"},
		{"download failed", ai.AttachmentPlacement{StockImage: 7, Path: "images/hero.jpg"}, &fakeStock{dlErr: stockimages.ErrUnavailable}, false, uses, "couldn't be downloaded"},
		{"saved but not used", ai.AttachmentPlacement{StockImage: 7, Path: "images/hero.jpg"}, &fakeStock{data: testJPEG}, false, "<p>no image</p>", "no file uses it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var saved [][]byte
			c := stockCatalog(tt.stock, &saved)
			snap := themecheck.Snapshot{Paths: map[string]bool{}}
			if tt.taken {
				snap.Paths[tt.placement.Path] = true
			}
			got := c.findings(context.Background(), []ai.AttachmentPlacement{tt.placement}, snap, []string{tt.used})
			if tt.want == "" {
				if len(got) != 0 {
					t.Fatalf("want no findings, got %+v", got)
				}
				return
			}
			if len(got) != 1 || !strings.Contains(got[0].Message, tt.want) || got[0].Rule != ruleIDUseAttachments {
				t.Fatalf("want one use-attachments finding containing %q, got %+v", tt.want, got)
			}
		})
	}
}

// An accepted stock placement stages exactly like an attached image: the photo, downloaded once, is saved as an
// attachment and its draft row points at that attachment.
func TestImageCatalog_PlanFilesSavesStockPhoto(t *testing.T) {
	stock := &fakeStock{data: testJPEG}
	var saved [][]byte
	c := stockCatalog(stock, &saved)
	p := ai.AttachmentPlacement{StockImage: 7, Path: "images/hero.jpg"}
	if got := c.findings(context.Background(), []ai.AttachmentPlacement{p}, themecheck.Snapshot{}, []string{"'images/hero.jpg'"}); len(got) != 0 {
		t.Fatalf("unexpected findings %+v", got)
	}
	store := mapThemeStore{files: map[string]string{"images/hero.jpg": ""}}
	files, err := c.planFiles(context.Background(), store, themefs.RequestAuth{}, []ai.AttachmentPlacement{p})
	if err != nil {
		t.Fatal(err)
	}
	want := "att-hero.jpg-image/jpeg"
	if len(files) != 1 || files[0].attachmentID != want || files[0].content != imageplacement.Reference(want) ||
		files[0].path != "images/hero.jpg" || files[0].action != FileActionCreate {
		t.Fatalf("got %+v, want a create row referencing %s", files, want)
	}
	if len(saved) != 1 || !bytes.Equal(saved[0], testJPEG) {
		t.Errorf("want the downloaded bytes saved once, got %d saves", len(saved))
	}
	if stock.downloads != 1 {
		t.Errorf("want one download shared by checking and staging, got %d", stock.downloads)
	}
}

func TestImageCatalog_StockPlacementOnATurnWithoutStock(t *testing.T) {
	c := newImageCatalog(nil, "", nil, nil)
	got := c.findings(context.Background(), []ai.AttachmentPlacement{{StockImage: 7, Path: "images/hero.jpg"}},
		themecheck.Snapshot{}, []string{"'images/hero.jpg'"})
	if len(got) != 1 || !strings.Contains(got[0].Message, "no stock photos are available") {
		t.Fatalf("got %+v", got)
	}
}

// Stock photos are saved into the theme, so enabling them never lets a page hotlink any host.
func TestImageHosts(t *testing.T) {
	s := &Service{}
	s.SetImageHosts([]string{" CDN.FlowPOS.example ", ""})
	s.SetStockImages(&fakeStock{})
	if len(s.imageHosts) != 1 || !s.imageHosts["cdn.flowpos.example"] {
		t.Fatalf("image hosts = %v, want only the platform host", s.imageHosts)
	}
}

// stockSearchingGenerator searches stock photos through the turn's own tool executor, then saves photo id into the
// theme at images/hero.jpg and uses it from a component.
type stockSearchingGenerator struct {
	fakeGenerator
	id          int
	toolOffered bool
}

func (g *stockSearchingGenerator) Generate(ctx context.Context, tc ai.ThemeContext, _ []ai.Turn, _ string, _ []ai.Image, _ ai.ToolProgress, toolExec ai.ToolExecutor, _ ai.FileReader) (*ai.Result, error) {
	g.toolOffered = tc.StockImages
	if _, err := toolExec(ctx, ai.ToolNameSearchStockImages, json.RawMessage(`{"query":"coffee beans","count":3}`)); err != nil {
		return nil, err
	}
	return &ai.Result{
		Summary:        "Redesigned the homepage with a coffee photo.",
		Files:          []ai.GeneratedFile{heroUsing("images/hero.jpg")},
		UseAttachments: []ai.AttachmentPlacement{{StockImage: g.id, Path: "images/hero.jpg"}},
	}, nil
}

// End to end: a searched photo is downloaded, stored as a hidden attachment, staged at images/hero.jpg, served by the
// preview and uploaded on Apply — the same pipeline as an attached image, and the page never hotlinks the provider.
func TestDoGenerate_StockPhotoIsSavedIntoTheTheme(t *testing.T) {
	f := newPlacementFixture(t)
	ctx := context.Background()
	stock := &fakeStock{photos: []stockimages.Photo{{ID: 7, Description: "coffee, beans"}}, data: testJPEG}
	f.svc.SetStockImages(stock)
	prompt := "redesign the homepage for my coffee shop"
	msg, err := f.chatSvc.RecordUserMessage(ctx, f.chat, &testUserID, "", "", prompt, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	gen := &stockSearchingGenerator{id: 7}
	f.svc.gen = gen
	genID := uuid.NewString()
	if err := f.repo.StartGeneration(ctx, genID, f.chat.ID, f.tenantID); err != nil {
		t.Fatal(err)
	}
	msgID := msg.ID
	in := GenerateInput{TenantID: f.tenantID, Token: "t", ThemeSlug: "test-theme", Prompt: prompt, UserMessageID: &msgID, redesign: true}
	if err := f.svc.doGenerate(ctx, in, f.chat, genID, nil); err != nil {
		t.Fatalf("doGenerate: %v", err)
	}
	if !gen.toolOffered {
		t.Fatal("a redesign turn with a provider configured must offer search_stock_images")
	}
	if stock.downloads != 1 {
		t.Errorf("want one download, got %d", stock.downloads)
	}
	if w := f.server.writes(); len(w) != 0 {
		t.Fatalf("staging must not reach FlowPOS, got %v", w)
	}
	rows := f.pendingAttachmentRows(t)
	if len(rows) != 1 || rows[0].FilePath != "images/hero.jpg" || rows[0].Action != FileActionCreate {
		t.Fatalf("want one staged images/hero.jpg row, got %+v", rows)
	}
	attachmentID, ok := imageplacement.ParseReference(rows[0].Content)
	if !ok {
		t.Fatalf("staged row must reference an attachment, got %q", rows[0].Content)
	}
	stored, err := f.chatSvc.GetChatImageAttachment(ctx, f.chat.ID, attachmentID)
	if err != nil || !bytes.Equal(stored.Content, testJPEG) || stored.MediaType != "image/jpeg" {
		t.Fatalf("stored photo = %q %q, %v; want the downloaded JPEG", stored.Content, stored.MediaType, err)
	}

	auth := themefs.RequestAuth{Token: "t", TenantID: f.tenantID}
	if data, err := f.svc.ReadPreviewAssetBytes(ctx, auth, "images/hero.jpg"); err != nil || !bytes.Equal(data, testJPEG) {
		t.Errorf("preview served %q, %v; want the downloaded photo", data, err)
	}

	if _, err := f.svc.ApplyDraft(ctx, f.tenantID, "tok", f.chat.ID, "test-theme"); err != nil {
		t.Fatalf("ApplyDraft: %v", err)
	}
	if w := f.server.writes(); len(w) < 2 || w[0] != "upload images/hero.jpg" {
		t.Fatalf("want the photo uploaded before the page write, got %v", w)
	}
	if !bytes.Equal(f.server.uploads["images/hero.jpg"], testJPEG) || f.server.types["images/hero.jpg"] != "image/jpeg" {
		t.Errorf("uploaded %q as %q, want the downloaded JPEG", f.server.uploads["images/hero.jpg"], f.server.types["images/hero.jpg"])
	}

	messages, err := f.chatSvc.ListMessages(ctx, f.tenantID, f.chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range messages {
		if len(m.Attachments) != 0 {
			t.Errorf("the transcript must not show the stock photo, got %+v on %s", m.Attachments, m.Role)
		}
	}
}

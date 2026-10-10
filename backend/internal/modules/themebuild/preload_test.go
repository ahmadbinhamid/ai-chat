package themebuild

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/aicatalog"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/prefetch"
	"ai-chat/internal/themefs"

	"github.com/google/uuid"
)

func strPtr(s string) *string { return &s }

func TestSanitizePreviewContext(t *testing.T) {
	tests := []struct {
		name      string
		route     *string
		focus     string
		wantRoute *string
		wantFocus string
	}{
		{"unknown route stays unknown", nil, "", nil, ""},
		{"home route is kept as empty", strPtr(""), "", strPtr(""), ""},
		{"route is trimmed", strPtr("  shop "), "components/header.liquid", strPtr("shop"), "components/header.liquid"},
		{"route with control characters is dropped", strPtr("shop\n<script>"), "", nil, ""},
		{"overlong route is dropped", strPtr(strings.Repeat("a", maxPreviewContextLen+1)), "", nil, ""},
		{"traversal focus file is dropped", strPtr("shop"), "../../etc/passwd", strPtr("shop"), ""},
		{"absolute focus file is dropped", nil, "/etc/passwd", nil, ""},
		{"backslash focus file is dropped", nil, `pages\home.liquid`, nil, ""},
		{"overlong focus file is dropped", nil, "pages/" + strings.Repeat("a", maxPreviewContextLen) + ".liquid", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			route, focus := sanitizePreviewContext(tt.route, tt.focus)
			if (route == nil) != (tt.wantRoute == nil) || (route != nil && *route != *tt.wantRoute) {
				t.Errorf("route = %v, want %v", route, tt.wantRoute)
			}
			if focus != tt.wantFocus {
				t.Errorf("focus = %q, want %q", focus, tt.wantFocus)
			}
		})
	}
}

func TestPreloadBlock(t *testing.T) {
	if got := preloadBlock(prefetch.Result{}); got != "" {
		t.Fatalf("empty preload produced a block: %q", got)
	}
	got := preloadBlock(prefetch.Result{Files: []prefetch.File{
		{Path: "pages/products.liquid", Content: "<h1>Shop</h1>"},
		{Path: "components/header.liquid", Content: "<header></header>"},
	}})
	for _, want := range []string{preloadHeading, "### pages/products.liquid\n<h1>Shop</h1>", "### components/header.liquid\n<header></header>"} {
		if !strings.Contains(got, want) {
			t.Errorf("block missing %q:\n%s", want, got)
		}
	}
}

func TestPromptWithAttachments_CarriesPreload(t *testing.T) {
	in := GenerateInput{Prompt: "make it darker", preload: prefetch.Result{Files: []prefetch.File{{Path: "pages/home.liquid", Content: "<h1>Hi</h1>"}}}}
	got := promptWithAttachments("make it darker", in)
	if !strings.HasPrefix(got, preloadHeading) || !strings.Contains(got, "### pages/home.liquid") || !strings.HasSuffix(got, "make it darker\n\n"+proposeInstruction) {
		t.Fatalf("prompt should open with the preload block and end with the request:\n%s", got)
	}
	if strings.Contains(promptWithAttachments("make it darker", GenerateInput{}), preloadHeading) {
		t.Fatal("prompt without a preload must not mention one")
	}
}

func TestExecReadThemeFile_PreloadedPaths(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{
		"pages/products.liquid":    "<h1>Shop</h1>",
		"components/header.liquid": "<header></header>",
		"components/footer.liquid": "<footer></footer>",
	})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}
	preloaded := map[string]string{
		"pages/products.liquid":    "<h1>Shop</h1>",
		"components/header.liquid": "<header>old</header>",
	}
	tests := []struct {
		name     string
		draft    map[string]string
		path     string
		wantNote bool
		wantBody string
	}{
		{"unchanged preloaded path gets the short note", nil, "pages/products.liquid", true, ""},
		{"preloaded path whose content differs gets the full file", nil, "components/header.liquid", false, "<header></header>"},
		{"a draft edit since the preload gets the full file", map[string]string{"pages/products.liquid": "<h1>Draft</h1>"}, "pages/products.liquid", false, "<h1>Draft</h1>"},
		{"a path that wasn't preloaded gets the full file", nil, "components/footer.liquid", false, "<footer></footer>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := themefs.NewOverlayStore(svc.store, tt.draft)
			exec := svc.buildToolExecutorWithPreload(store, testStoreAuth(), toolOptions{preloaded: preloaded})
			input, _ := json.Marshal(readThemeFileInput{Paths: []string{tt.path}})
			out, err := exec(context.Background(), "read_theme_file", input)
			if err != nil {
				t.Fatalf("read_theme_file failed: %v", err)
			}
			if gotNote := strings.Contains(out, "already provided above"); gotNote != tt.wantNote {
				t.Fatalf("note = %v, want %v:\n%s", gotNote, tt.wantNote, out)
			}
			if tt.wantBody != "" && !strings.Contains(out, tt.wantBody) {
				t.Fatalf("output missing the current content %q:\n%s", tt.wantBody, out)
			}
		})
	}
}

func TestPreloadFiles(t *testing.T) {
	pages := `[{"title":"Shop","slug":"shop","path":"/pages","page":"products"}]`
	ts := newFakeThemeServer(t, map[string]string{
		"pages/products.liquid":    "{% render 'components/header' %}{% render 'components/banner' %}",
		"components/header.liquid": "<header>saved</header>",
		"components/banner.liquid": "<div>banner</div>",
	})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}
	draft := map[string]string{"components/header.liquid": "<header>draft</header>"}

	tests := []struct {
		name      string
		in        GenerateInput
		staged    map[string]bool
		wantPaths []string
	}{
		{"route page and its components, read through the draft", GenerateInput{PreviewRoute: strPtr("shop")}, nil,
			[]string{"pages/products.liquid", "components/header.liquid", "components/banner.liquid"}},
		{"brand mode preloads nothing", GenerateInput{PreviewRoute: strPtr("shop"), Mode: ai.GenerationModeBrand}, nil, nil},
		{"unknown route preloads nothing", GenerateInput{}, nil, nil},
		{"staged images are skipped", GenerateInput{PreviewRoute: strPtr("shop")}, map[string]bool{"components/banner.liquid": true},
			[]string{"pages/products.liquid", "components/header.liquid"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := ai.ThemeContext{PagesJSON: pages, StagedImagePaths: tt.staged}
			got := svc.preloadFiles(context.Background(), tt.in, tc, themefs.NewOverlayStore(svc.store, draft), testStoreAuth())
			var paths []string
			for _, f := range got.Files {
				paths = append(paths, f.Path)
				if f.Path == "components/header.liquid" && f.Content != "<header>draft</header>" {
					t.Errorf("header was read from the saved theme, not the draft: %q", f.Content)
				}
			}
			if strings.Join(paths, ",") != strings.Join(tt.wantPaths, ",") {
				t.Fatalf("paths = %v, want %v", paths, tt.wantPaths)
			}
		})
	}
}

func TestRepository_GenerationKeepsPreviewContext(t *testing.T) {
	tests := []struct {
		name      string
		route     *string
		focus     string
		wantRoute *string
	}{
		{"unknown", nil, "", nil},
		{"home", strPtr(""), "", strPtr("")},
		{"route and focus", strPtr("shop"), "components/header.liquid", strPtr("shop")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := NewRepository(openTestDB(t))
			chatID := uuid.NewString()
			if _, err := repo.EnqueueGeneration(context.Background(), Generation{
				ID: uuid.NewString(), ChatID: chatID, TenantID: 1, Prompt: "p", ThemeSlug: "shop",
				PreviewRoute: tt.route, FocusFile: tt.focus,
			}); err != nil {
				t.Fatalf("EnqueueGeneration failed: %v", err)
			}
			g, err := repo.DequeueNext(context.Background(), chatID)
			if err != nil {
				t.Fatalf("DequeueNext failed: %v", err)
			}
			defer func() { _ = repo.EndGeneration(context.Background(), chatID, g.ID, nil) }()
			if (g.PreviewRoute == nil) != (tt.wantRoute == nil) || (g.PreviewRoute != nil && *g.PreviewRoute != *tt.wantRoute) {
				t.Errorf("PreviewRoute = %v, want %v", g.PreviewRoute, tt.wantRoute)
			}
			if g.FocusFile != tt.focus {
				t.Errorf("FocusFile = %q, want %q", g.FocusFile, tt.focus)
			}
		})
	}
}

// promptRecorder records every prompt it's sent and answers with a no-change reply.
type promptRecorder struct {
	mu      sync.Mutex
	prompts []string
}

func (g *promptRecorder) Generate(_ context.Context, _ ai.ThemeContext, _ []ai.Turn, prompt string, _ []ai.Image, _ ai.ToolProgress, _ ai.ToolExecutor, _ ai.FileReader) (*ai.Result, error) {
	g.mu.Lock()
	g.prompts = append(g.prompts, prompt)
	g.mu.Unlock()
	return &ai.Result{Summary: "ok", AnsweredQuestion: true}, nil
}

func (g *promptRecorder) first() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.prompts) == 0 {
		return ""
	}
	return g.prompts[0]
}

func (*promptRecorder) Summarize(context.Context, string, []ai.Turn) (string, error) { return "", nil }

func (*promptRecorder) SupportsVision() bool { return false }

// Through the queue: the preview context sent with a message reaches the model's first prompt as preloaded files.
func TestGenerate_PreloadsTheViewedPageIntoTheFirstPrompt(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{
		"pages.json":               `[{"title":"Shop","slug":"shop","path":"/pages","page":"products"}]`,
		"pages/products.liquid":    "<main>{% render 'components/grid' %}</main>",
		"components/grid.liquid":   "<div class=\"grid\"></div>",
		"components/footer.liquid": "<footer></footer>",
	})
	defer ts.Close()
	conn := openTestDB(t)
	chatSvc := chat.NewService(chat.NewRepository(conn))
	svc := NewService(NewRepository(conn), chatSvc, nil, themefs.NewStore(ts.URL), nil)
	gen := &promptRecorder{}
	svc.gen = gen

	tenantID := uint64(time.Now().UnixNano())
	out, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "shop", Prompt: "make the grid wider",
		PreviewRoute: strPtr("shop"), FocusFile: "components/footer.liquid",
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	waitFor(t, "the generation to finish", func() bool {
		return generationStatus(t, svc, out.Chat.ID, out.GenerationID) == GenerationStatusSucceeded
	})

	prompt := gen.first()
	for _, want := range []string{
		"make the grid wider", preloadHeading,
		"### pages/products.liquid\n<main>", "### components/footer.liquid", "### components/grid.liquid",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("first prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Index(prompt, "pages/products.liquid") > strings.Index(prompt, "components/footer.liquid") {
		t.Error("the route page must come before the focus file")
	}

	var m GenerationMetrics
	waitFor(t, "the metrics row", func() bool {
		var ok bool
		m, ok = readGenerationMetrics(t, conn, out.GenerationID)
		return ok
	})
	if m.PreloadedFiles != 3 || m.PreloadedBytes == 0 {
		t.Errorf("metrics preloaded = %d files / %d bytes, want 3 files and some bytes", m.PreloadedFiles, m.PreloadedBytes)
	}
}

func TestPreloadFiles_ModelSwitch(t *testing.T) {
	pages := `[{"title":"Shop","slug":"shop","path":"/pages","page":"products"}]`
	ts := newFakeThemeServer(t, map[string]string{"pages/products.liquid": "<h1>Shop</h1>"})
	defer ts.Close()
	catalog, err := aicatalog.Parse([]byte(`{
		"providers": {"p": {"base_url": "https://p.test", "api_key_env": "K"}},
		"models": [
			{"id": "on", "label": "On", "provider": "p", "model": "m-on", "thinking": true, "efforts": ["low"], "default_effort": "low"},
			{"id": "off", "label": "Off", "provider": "p", "model": "m-off", "thinking": true, "efforts": ["low"], "default_effort": "low", "preload": false}
		],
		"default_model": "on", "summary_model": "on"
	}`), func(string) (string, bool) { return "k", true })
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	tests := []struct {
		name    string
		models  *aicatalog.Catalog
		modelID string
		want    int
	}{
		{"model with preload on", catalog, "on", 1},
		{"model with preload off", catalog, "off", 0},
		{"unknown model keeps preload on", catalog, "gone", 1},
		{"no catalogue keeps preload on", nil, "off", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &Service{store: themefs.NewStore(ts.URL), models: tt.models}
			in := GenerateInput{PreviewRoute: strPtr("shop"), model: aicatalog.Choice{ModelID: tt.modelID}}
			got := svc.preloadFiles(context.Background(), in, ai.ThemeContext{PagesJSON: pages}, svc.store, testStoreAuth())
			if len(got.Files) != tt.want {
				t.Fatalf("preloaded %d files, want %d", len(got.Files), tt.want)
			}
		})
	}
}

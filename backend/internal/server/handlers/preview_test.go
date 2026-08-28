package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-chat/internal/modules/chat"
	"ai-chat/internal/modules/themebuild"
	"ai-chat/internal/themefs"

	"github.com/gin-gonic/gin"
)

// fakePreviewThemeServer serves a fixed, small theme (list + read only —
// preview never writes) for the preview handler to render against.
func fakePreviewThemeServer(files map[string]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/store/themes/active/files" {
			entries := make([]themefs.FileTreeEntry, 0, len(files))
			for p := range files {
				entries = append(entries, themefs.FileTreeEntry{Name: p, Path: p, Type: "file"})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"files": entries}})
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/store/themes/active/files/")
		content, ok := files[path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"path": path, "content": content, "encoding": "utf-8"}})
	}))
}

func TestPreviewHandler_RendersSavedPage(t *testing.T) {
	ts := fakePreviewThemeServer(map[string]string{
		"liquid/layout-start.liquid": `<html><head><title>{{ page.seo_title }}</title></head><body>`,
		"liquid/layout-end.liquid":   `</body></html>`,
		"pages/home.liquid": `{% render 'liquid/layout-start', page: page, store: store, menu: menu, path: path, theme: theme, customer: customer, customer_authenticated: auth_check, environment: environment, csrf_token: csrf_token %}
<h1>{{ store.name }}</h1>
{% if product.on_sale == true or product.on_sale == 1 %}<span>On sale: {{ product.price_formatted }}</span>{% endif %}
{% render 'liquid/layout-end', theme: theme, store: store %}`,
	})
	defer ts.Close()

	buildSvc := themebuild.NewService(nil, chat.NewService(nil), nil, themefs.NewStore(ts.URL), nil)

	router := gin.New()
	router.Use(fakeAuthMiddleware(1))
	router.POST("/themes/:slug/preview", NewPreviewHandler(buildSvc).Preview)

	body, _ := json.Marshal(previewRequest{Page: "home"})
	req := httptest.NewRequest(http.MethodPost, "/themes/demo/preview", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data previewResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v (%s)", err, rec.Body.String())
	}
	if len(resp.Data.Errors) != 0 {
		t.Errorf("expected no render errors, got %+v", resp.Data.Errors)
	}
	if !strings.Contains(resp.Data.HTML, "Sample Store") {
		t.Errorf("expected the fixture store name in the output, got %q", resp.Data.HTML)
	}
	if !strings.Contains(resp.Data.HTML, "On sale") {
		t.Errorf("expected the on-sale branch to render (fixture product.on_sale=true), got %q", resp.Data.HTML)
	}
}

func TestPreviewHandler_RendersUnsavedDraftContent(t *testing.T) {
	ts := fakePreviewThemeServer(map[string]string{
		"liquid/layout-start.liquid": `<body>`,
		"liquid/layout-end.liquid":   `</body>`,
	})
	defer ts.Close()

	buildSvc := themebuild.NewService(nil, chat.NewService(nil), nil, themefs.NewStore(ts.URL), nil)

	router := gin.New()
	router.Use(fakeAuthMiddleware(1))
	router.POST("/themes/:slug/preview", NewPreviewHandler(buildSvc).Preview)

	draft := `{% render 'liquid/layout-start', page: page, store: store, menu: menu, path: path, theme: theme, customer: customer, customer_authenticated: auth_check, environment: environment, csrf_token: csrf_token %}
<p>Draft: {{ store.name }}</p>
{% render 'liquid/layout-end', theme: theme, store: store %}`
	body, _ := json.Marshal(previewRequest{Path: "pages/draft.liquid", Files: map[string]string{"pages/draft.liquid": draft}})
	req := httptest.NewRequest(http.MethodPost, "/themes/demo/preview", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data previewResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !strings.Contains(resp.Data.HTML, "Draft: Sample Store") {
		t.Errorf("expected the unsaved draft content to render, got %q", resp.Data.HTML)
	}
}

// TestPreviewHandler_UsesRealStoreName covers buildPreviewContext's overlay:
// both Preview and Context must report the tenant's real store name (from
// flowpos-backend's GET /store), not FixtureContext's canned "Sample Store"
// — and, critically, the SAME name from both, since PreviewPane's "Check
// accuracy" button diffs one against the other (see buildPreviewContext's
// own doc comment on why a mismatch between the two would be a regression,
// not just a missed opportunity).
func TestPreviewHandler_UsesRealStoreName(t *testing.T) {
	files := map[string]string{
		"liquid/layout-start.liquid": `<body>`,
		"liquid/layout-end.liquid":   `</body>`,
		"pages/home.liquid": `{% render 'liquid/layout-start', page: page, store: store, menu: menu, path: path, theme: theme, customer: customer, customer_authenticated: auth_check, environment: environment, csrf_token: csrf_token %}
<h1>{{ store.name }}</h1>
{% render 'liquid/layout-end', theme: theme, store: store %}`,
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/store" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"store": map[string]any{"name": "Fleure"}}})
			return
		}
		if r.URL.Path == "/store/themes/active/files" {
			entries := make([]themefs.FileTreeEntry, 0, len(files))
			for p := range files {
				entries = append(entries, themefs.FileTreeEntry{Name: p, Path: p, Type: "file"})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"files": entries}})
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/store/themes/active/files/")
		content, ok := files[path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"path": path, "content": content, "encoding": "utf-8"}})
	}))
	defer ts.Close()

	buildSvc := themebuild.NewService(nil, chat.NewService(nil), nil, themefs.NewStore(ts.URL), nil)
	router := gin.New()
	router.Use(fakeAuthMiddleware(1))
	previewHandler := NewPreviewHandler(buildSvc)
	router.POST("/themes/:slug/preview", previewHandler.Preview)
	router.GET("/preview/context", previewHandler.Context)

	body, _ := json.Marshal(previewRequest{Page: "home"})
	req := httptest.NewRequest(http.MethodPost, "/themes/demo/preview", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data previewResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !strings.Contains(resp.Data.HTML, "Fleure") {
		t.Errorf("expected the real store name to render, got %q", resp.Data.HTML)
	}

	ctxReq := httptest.NewRequest(http.MethodGet, "/preview/context", nil)
	ctxRec := httptest.NewRecorder()
	router.ServeHTTP(ctxRec, ctxReq)
	var ctxResp struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(ctxRec.Body.Bytes(), &ctxResp); err != nil {
		t.Fatalf("failed to decode context response: %v", err)
	}
	store, _ := ctxResp.Data["store"].(map[string]any)
	if store["name"] != "Fleure" {
		t.Errorf("expected GET /preview/context to also report the real store name, got %+v", ctxResp.Data["store"])
	}
}

func TestPreviewHandler_MissingTargetIsBadRequest(t *testing.T) {
	router := gin.New()
	router.Use(fakeAuthMiddleware(1))
	router.POST("/themes/:slug/preview", NewPreviewHandler(themebuild.NewService(nil, chat.NewService(nil), nil, themefs.NewStore("http://unused.invalid"), nil)).Preview)

	body, _ := json.Marshal(previewRequest{})
	req := httptest.NewRequest(http.MethodPost, "/themes/demo/preview", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

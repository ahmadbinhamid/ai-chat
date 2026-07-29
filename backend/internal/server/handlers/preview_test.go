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
	body, _ := json.Marshal(previewRequest{Path: "pages/draft.liquid", Content: draft})
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

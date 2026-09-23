package handlers

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-chat/internal/modules/chat"
	"ai-chat/internal/modules/themebuild"
	"ai-chat/internal/themefs"

	"github.com/gin-gonic/gin"
)

// fakeAssetThemeServer serves one fixed binary file's bytes, base64-encoded like flowpos-backend's real file API.
func fakeAssetThemeServer(relPath string, data []byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"path":     relPath,
				"content":  base64.StdEncoding.EncodeToString(data),
				"encoding": "base64",
			},
		})
	}))
}

func TestAssetHandler_SetsETagAndServesConditionalGet(t *testing.T) {
	data := []byte("fake-png-bytes")
	ts := fakeAssetThemeServer("images/logo.png", data)
	defer ts.Close()

	buildSvc := themebuild.NewService(nil, chat.NewService(nil), nil, themefs.NewStore(ts.URL), nil)
	router := gin.New()
	// Uses 2, not 1, so golangci-lint's unparam doesn't flag this param as unvarying across call sites.
	router.Use(fakeAuthMiddleware(2))
	router.GET("/theme-assets/*path", NewAssetHandler(buildSvc).Get)

	req := httptest.NewRequest(http.MethodGet, "/theme-assets/images/logo.png", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != string(data) {
		t.Errorf("expected raw bytes %q, got %q", data, rec.Body.String())
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("expected an ETag header on the first response")
	}
	if cc := rec.Header().Get("Cache-Control"); cc == "" {
		t.Error("expected a Cache-Control header")
	}

	// Repeating the request with the ETag (browser revalidation) should short-circuit to 304.
	req2 := httptest.NewRequest(http.MethodGet, "/theme-assets/images/logo.png", nil)
	req2.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusNotModified {
		t.Fatalf("expected 304 on a matching If-None-Match, got %d", rec2.Code)
	}
	if rec2.Body.Len() != 0 {
		t.Errorf("expected an empty body on 304, got %d bytes", rec2.Body.Len())
	}

	// A stale/wrong If-None-Match must still get the full response.
	req3 := httptest.NewRequest(http.MethodGet, "/theme-assets/images/logo.png", nil)
	req3.Header.Set("If-None-Match", `"stale-etag"`)
	rec3 := httptest.NewRecorder()
	router.ServeHTTP(rec3, req3)

	if rec3.Code != http.StatusOK {
		t.Fatalf("expected 200 on a non-matching If-None-Match, got %d", rec3.Code)
	}
	if rec3.Body.String() != string(data) {
		t.Errorf("expected raw bytes %q, got %q", data, rec3.Body.String())
	}
}

package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"ai-chat/internal/ai"
	"ai-chat/internal/aicatalog"
	"ai-chat/internal/modules/themebuild"
	"ai-chat/internal/ratelimit"
)

func catalogueService(t *testing.T) *themebuild.Service {
	t.Helper()
	data, err := os.ReadFile("../../../config/ai-models.deepseek.json")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := aicatalog.Parse(data, func(string) (string, bool) { return "k", true })
	if err != nil {
		t.Fatal(err)
	}
	svc := themebuild.NewService(nil, nil, ai.NewFake(0), nil, nil)
	svc.SetModelCatalog(cat)
	return svc
}

func TestModels_ListsChoicesWithoutProviderDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/models", NewModelsHandler(catalogueService(t)).List)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/models", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, secret := range []string{"deepseek-v4", "api.deepseek.com", "AI_API_KEY", "base_url", "provider", "deepseek-flash-vision"} {
		if strings.Contains(body, secret) {
			t.Errorf("GET /models must not expose %q: %s", secret, body)
		}
	}
	var out struct {
		Data aicatalog.Public `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if len(out.Data.Models) != 2 || out.Data.Auto == nil || out.Data.DefaultModel != aicatalog.AutoID {
		t.Errorf("want Auto plus the two selectable models, got %+v", out.Data)
	}
}

func TestSend_RejectsModelsOutsideTheCatalogue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewMessageHandler(catalogueService(t), ratelimit.NewPerTenantLimiter(100))
	r := gin.New()
	r.POST("/chats/messages", h.Send)

	tests := []struct{ name, body string }{
		{"unknown model", `{"theme_slug":"shop","prompt":"hi","model":"gpt-5"}`},
		{"provider model name", `{"theme_slug":"shop","prompt":"hi","model":"deepseek-v4-pro"}`},
		{"internal-only model", `{"theme_slug":"shop","prompt":"hi","model":"deepseek-flash-vision"}`},
		{"effort the model doesn't offer", `{"theme_slug":"shop","prompt":"hi","model":"deepseek-pro","effort":"max"}`},
		{"effort with auto", `{"theme_slug":"shop","prompt":"hi","model":"auto","effort":"high"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/chats/messages", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "INVALID_MODEL_CHOICE") {
				t.Errorf("want 400 INVALID_MODEL_CHOICE, got %d: %s", w.Code, w.Body)
			}
		})
	}
}

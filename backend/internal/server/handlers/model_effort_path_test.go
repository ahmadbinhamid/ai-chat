package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"ai-chat/internal/ai"
	"ai-chat/internal/aicatalog"
	"ai-chat/internal/auth"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/modules/themebuild"
	"ai-chat/internal/ratelimit"
	"ai-chat/internal/themefs"
)

// staticThemeStore is a read-only theme for a generation that changes nothing.
type staticThemeStore map[string]string

func (s staticThemeStore) ReadFile(_ context.Context, _ themefs.RequestAuth, p string) (string, error) {
	if c, ok := s[p]; ok {
		return c, nil
	}
	return "", errors.New("not found")
}
func (staticThemeStore) WriteFile(context.Context, themefs.RequestAuth, string, string, *themefs.PageMeta) error {
	return errors.New("read-only")
}
func (staticThemeStore) DeleteFile(context.Context, themefs.RequestAuth, string) error {
	return errors.New("read-only")
}
func (staticThemeStore) ListFiles(context.Context, themefs.RequestAuth) ([]themefs.FileTreeEntry, error) {
	return nil, nil
}
func (staticThemeStore) UploadFile(context.Context, themefs.RequestAuth, string, []byte, string) error {
	return errors.New("read-only")
}

// The whole path for "DeepSeek Pro + Thorough": the request body's model and effort must reach the provider call.
func TestSend_ChosenModelAndEffortReachTheProviderCall(t *testing.T) {
	conn := openStreamTestDB(t)
	tenantID := uint64(time.Now().UnixNano())

	type providerCall struct {
		Model        string `json:"model"`
		OutputConfig *struct {
			Effort string `json:"effort"`
		} `json:"output_config"`
	}
	var mu sync.Mutex
	var calls []providerCall
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var call providerCall
		_ = json.Unmarshal(body, &call)
		mu.Lock()
		calls = append(calls, call)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		input, _ := json.Marshal(map[string]any{
			"summary": "Here is what I found.", "needs_clarification": false, "answered_question": true,
			"files": []any{}, "page_registry_entry": nil, "layout_links_to_add": []string{}, "layout_scripts_to_add": []string{},
		})
		fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n"+
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"propose_changes\",\"input\":{}}}\n\n"+
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":%q}}\n\n"+
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":5}}\n\n"+
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", string(input))
	}))
	defer provider.Close()

	data, err := os.ReadFile("../../../config/ai-models.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	_ = json.Unmarshal(data, &raw)
	raw["providers"].(map[string]any)["deepseek"].(map[string]any)["base_url"] = provider.URL
	data, _ = json.Marshal(raw)
	lookup := func(string) (string, bool) { return "k", true }
	cat, err := aicatalog.Parse(data, lookup)
	if err != nil {
		t.Fatal(err)
	}
	gen, err := ai.New(cat, lookup, 0, ai.StreamTimeouts{})
	if err != nil {
		t.Fatal(err)
	}

	chatSvc := chat.NewService(chat.NewRepository(conn))
	store := staticThemeStore{
		"pages.json": "[]", "defaults.json": "{}",
		"liquid/layout-start.liquid": "<html><head></head><body>", "liquid/layout-end.liquid": "</body></html>",
	}
	svc := themebuild.NewService(themebuild.NewRepository(conn), chatSvc, gen, store, nil)
	svc.SetModelCatalog(cat)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Stands in for auth.Middleware, which sets the identity under this key after verifying the token with FlowPOS.
	r.Use(func(c *gin.Context) {
		c.Set("auth_identity", auth.Identity{UserID: 1, TenantID: tenantID})
		c.Set("auth_token", "t")
	})
	r.POST("/chats/messages", NewMessageHandler(svc, ratelimit.NewPerTenantLimiter(100)).Send)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/chats/messages",
		strings.NewReader(`{"theme_slug":"shop","prompt":"redesign hero section","model":"deepseek-pro","effort":"high"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("Send: %d %s", w.Code, w.Body)
	}

	var reply *chat.Message
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && reply == nil; time.Sleep(20 * time.Millisecond) {
		c, err := chatSvc.GetChatForTenant(context.Background(), tenantID, themebuild.ChatType)
		if err != nil {
			continue
		}
		messages, _ := chatSvc.ListMessages(context.Background(), tenantID, c.ID)
		for i := range messages {
			if messages[i].Role == chat.RoleAssistant {
				reply = &messages[i]
			}
		}
	}
	if reply == nil {
		t.Fatal("timed out waiting for the reply")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) == 0 || calls[0].Model != "deepseek-v4-pro" || calls[0].OutputConfig == nil || calls[0].OutputConfig.Effort != "high" {
		t.Fatalf("want the provider called with deepseek-v4-pro at high, got %+v", calls)
	}
	if reply.ModelID == nil || *reply.ModelID != "deepseek-pro" || reply.Effort == nil || *reply.Effort != "high" {
		t.Errorf("want the reply recorded as deepseek-pro/high, got %v/%v", reply.ModelID, reply.Effort)
	}
}

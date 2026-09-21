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

// TestDoGenerate_ProtectsBlogPageFromSideEffectPagesJSONRewrite drives
// protectPages through the real doGenerate. The scenario is a direct
// pages.json rewrite (Action "update" — a plain, always-valid file edit;
// see protectPages' own doc comment on why this, not a delete action, is
// the actually-reachable vector in this codebase: validateProposal only
// accepts create/update) that silently drops the blog listing's row while
// ostensibly just updating the pricing page's entry. The prompt never asks
// for the blog page to go, so pages.json must land unchanged, and the
// merchant-visible summary must say so rather than silently look like the
// request was carried out in full.
func TestDoGenerate_ProtectsBlogPageFromSideEffectPagesJSONRewrite(t *testing.T) {
	conn := openTestDB(t)
	chatRepo := chat.NewRepository(conn)
	chatSvc := chat.NewService(chatRepo)
	buildRepo := NewRepository(conn)

	currentPagesJSON := `[{"slug":"blog","page":"blog","status":"published"},{"slug":"pricing","page":"pricing","status":"published"}]`
	rewrittenPagesJSON := `[{"slug":"pricing","page":"pricing","status":"published","seo_title":"New pricing title"}]`

	storeServer := newFakeThemeServer(t, map[string]string{
		"pages/blog.liquid":          "BLOG LISTING",
		"pages/pricing.liquid":       "PRICING",
		"pages.json":                 currentPagesJSON,
		"defaults.json":              `{}`,
		"liquid/layout-start.liquid": "<html>",
		"liquid/layout-end.liquid":   "</html>",
	})
	defer storeServer.Close()

	gen := &fakeGenerator{results: []*ai.Result{{
		Summary: "Updated the pricing page's SEO title.",
		Files: []ai.GeneratedFile{
			{Path: "pages.json", Action: "update", Content: rewrittenPagesJSON},
		},
	}}}

	svc := NewService(buildRepo, chatSvc, nil, themefs.NewStore(storeServer.URL), nil)
	svc.gen = gen

	tenantID := uint64(time.Now().UnixNano())
	ctx := context.Background()
	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}

	genID := uuid.NewString()
	if err := buildRepo.StartGeneration(ctx, genID, c.ID, tenantID); err != nil {
		t.Fatalf("StartGeneration failed: %v", err)
	}

	in := GenerateInput{TenantID: tenantID, Token: "t", ThemeSlug: "test-theme", Prompt: "update the pricing page's SEO title"}
	if err := svc.doGenerate(ctx, in, c, genID, nil); err != nil {
		t.Fatalf("doGenerate returned an error: %v", err)
	}

	events, err := buildRepo.GetEventsSince(ctx, c.ID, 0)
	if err != nil {
		t.Fatalf("GetEventsSince failed: %v", err)
	}
	var stagedPaths []string
	var sawStaged bool
	for _, ev := range events {
		if ev.Type != EventTypeStaged {
			continue
		}
		sawStaged = true
		var payload struct {
			Paths []string `json:"paths"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("unmarshal staged payload: %v", err)
		}
		stagedPaths = payload.Paths
	}
	if !sawStaged {
		t.Fatalf("expected pages.json's update to still be staged (reverted content, not dropped entirely), got events: %+v", events)
	}
	found := false
	for _, p := range stagedPaths {
		if p == "pages.json" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected pages.json to still be staged, got: %v", stagedPaths)
	}

	messages, err := chatSvc.ListMessagesForVerifiedChat(ctx, c.ID)
	if err != nil {
		t.Fatalf("ListMessagesForVerifiedChat failed: %v", err)
	}
	var assistantMsg *chat.Message
	for i := range messages {
		if messages[i].Role == chat.RoleAssistant {
			assistantMsg = &messages[i]
		}
	}
	if assistantMsg == nil {
		t.Fatal("expected an assistant message to be recorded")
	}
	if !strings.Contains(assistantMsg.Content, "protected") {
		t.Errorf("expected the reply to explain the blog page was protected, got %q", assistantMsg.Content)
	}
}

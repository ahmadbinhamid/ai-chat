package themebuild

import (
	"context"
	"strings"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
)

// The 11 lines the 19:39 forced round resubmitted: identical apart from CRLF → LF.
const crlfButtonBlock = "              <button\r\n" +
	"                type=\"button\"\r\n" +
	"                class=\"t1-pgb-cta\"\r\n" +
	"                data-pgb-add-to-cart\r\n" +
	"                data-product-id=\"{{ product.id }}\"\r\n" +
	"                data-variant-id=\"{{ product.default_variant_id }}\"\r\n" +
	"              >Add to Cart</button>\r\n" +
	"              {% endif %}\r\n" +
	"            </div>\r\n" +
	"          </article>\r\n" +
	"            {% endif %}\r\n"

func TestDoGenerate_WhitespaceOnlyProposal(t *testing.T) {
	const path = "components/product-grid-block.liquid"
	saved := "{% for product in products.items %}\n<article>\n<div>\n{% if product.can_quick_add == 1 %}\n" +
		crlfButtonBlock + "{% endfor %}\n"
	lfOnly := strings.ReplaceAll(saved, "\r\n", "\n")

	tests := []struct {
		name        string
		prompt      string
		wantReply   string
		wantStaged  bool
		wantPending chat.ApplyStatus
	}{
		{name: "forced follow-up's line-ending edit is no change", prompt: "still not working",
			wantReply: nothingChangedReply, wantPending: chat.ApplyStatusNotApplicable},
		{name: "explicit formatting request is staged", prompt: "convert the line endings in the product grid to LF",
			wantReply: "Restored the missing data-variant-id attribute.", wantStaged: true, wantPending: chat.ApplyStatusPending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := openTestDB(t)
			chatSvc := chat.NewService(chat.NewRepository(conn))
			store := mapThemeStore{files: map[string]string{
				"pages.json": "[]", "defaults.json": "{}",
				"liquid/layout-start.liquid": "<html><head></head><body>", "liquid/layout-end.liquid": "</body></html>",
				path: saved,
			}}
			svc := NewService(NewRepository(conn), chatSvc, nil, store, nil)
			svc.gen = &fakeGenerator{results: []*ai.Result{{
				Summary: "Restored the missing data-variant-id attribute.",
				Files:   []ai.GeneratedFile{{Path: path, Action: "update", Content: lfOnly}},
			}}}
			tenantID := uint64(time.Now().UnixNano())

			outcome, err := svc.Generate(context.Background(), GenerateInput{TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "demo", Prompt: tt.prompt})
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			waitForAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)

			messages, err := chatSvc.ListMessages(context.Background(), tenantID, outcome.Chat.ID)
			if err != nil {
				t.Fatalf("ListMessages: %v", err)
			}
			reply := messages[len(messages)-1]
			if !strings.HasPrefix(reply.Content, tt.wantReply) || reply.ApplyStatus != tt.wantPending {
				t.Errorf("got reply %q (%s), want %q (%s)", reply.Content, reply.ApplyStatus, tt.wantReply, tt.wantPending)
			}
			draft, err := svc.repo.DraftFiles(context.Background(), outcome.Chat.ID)
			if err != nil {
				t.Fatalf("DraftFiles: %v", err)
			}
			if _, staged := draft[path]; staged != tt.wantStaged {
				t.Errorf("staged = %v, want %v (draft %v)", staged, tt.wantStaged, draft)
			}
		})
	}
}

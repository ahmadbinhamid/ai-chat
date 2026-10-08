package themebuild

import (
	"context"
	"strings"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/themecheck"
	"ai-chat/internal/themefs"
)

func TestDraftReplacementNotes(t *testing.T) {
	reverted := strings.Replace(reversionSaved, "padding: 16px;", "padding: 16px;\n  z-index: 10;", 1)
	svc := &Service{store: mapThemeStore{files: map[string]string{reversionPath: reversionSaved}}}
	got := svc.draftReplacementNotes(context.Background(), themefs.RequestAuth{}, "chat-1",
		map[string]string{reversionPath: reversionDraft}, reversionResult(reverted))
	if len(got) != 1 || got[0].Severity != themecheck.SeverityWarning || got[0].Path != reversionPath {
		t.Fatalf("want one note for %s, got %+v", reversionPath, got)
	}
	note := appendWarningsNote("Redesigned the header.", got)
	if !strings.Contains(note, reversionPath+": Your earlier unsaved changes to this file were replaced by this redesign.") {
		t.Errorf("the merchant must be told the earlier work was replaced, got %q", note)
	}
}

// End to end: a redesign that replaces earlier unsaved work is staged (no repair, no failure) and the merchant's reply
// says so; a redesign that keeps the earlier work gets no note.
func TestDoGenerate_RedesignReplacesDraftWithANote(t *testing.T) {
	reverted := strings.Replace(reversionSaved, "padding: 16px;", "padding: 16px;\n  z-index: 10;", 1)
	kept := strings.Replace(reversionDraft, "padding: 16px;", "padding: 16px;\n  z-index: 10;", 1)
	tests := []struct {
		name     string
		proposed string
		wantNote bool
	}{
		{name: "earlier work replaced", proposed: reverted, wantNote: true},
		{name: "earlier work kept", proposed: kept},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := openTestDB(t)
			chatSvc := chat.NewService(chat.NewRepository(conn))
			store := mapThemeStore{files: map[string]string{
				"pages.json": "[]", "defaults.json": "{}",
				"liquid/layout-start.liquid": "<html><head></head><body>", "liquid/layout-end.liquid": "</body></html>",
				reversionPath: reversionSaved,
			}}
			gen := &promptRecordingGenerator{fakeGenerator: fakeGenerator{results: []*ai.Result{
				{Summary: "Made the header dark.", Files: []ai.GeneratedFile{{Path: reversionPath, Action: "update", Content: reversionDraft}}},
				{Summary: "Redesigned the header.", Files: []ai.GeneratedFile{{Path: reversionPath, Action: "update", Content: tt.proposed}}},
			}}}
			svc := NewService(NewRepository(conn), chatSvc, nil, store, nil)
			svc.gen = gen
			tenantID := uint64(time.Now().UnixNano())

			var chatID string
			for i, prompt := range []string{"make the header background dark", "redesign the header completely"} {
				outcome, err := svc.Generate(context.Background(), GenerateInput{TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "demo", Prompt: prompt})
				if err != nil {
					t.Fatalf("Generate(%q): %v", prompt, err)
				}
				chatID = outcome.Chat.ID
				waitForAssistantReplies(t, chatSvc, tenantID, chatID, i+1)
			}

			if len(gen.prompts) != 2 {
				t.Fatalf("a redesign must not go to repair: want 2 model calls, got %d", len(gen.prompts))
			}
			messages, err := chatSvc.ListMessages(context.Background(), tenantID, chatID)
			if err != nil {
				t.Fatal(err)
			}
			reply := messages[len(messages)-1]
			if reply.Status != chat.MessageStatusCompleted || reply.ApplyStatus != chat.ApplyStatusPending {
				t.Fatalf("want the redesign staged, got %s / %s: %q", reply.Status, reply.ApplyStatus, reply.Content)
			}
			hasNote := strings.Contains(reply.Content, "Your earlier unsaved changes to this file were replaced by this redesign.")
			if hasNote != tt.wantNote {
				t.Errorf("note present = %v, want %v; reply %q", hasNote, tt.wantNote, reply.Content)
			}
		})
	}
}

package themebuild

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"

	"github.com/google/uuid"
)

func TestCommitTurn_IsAllOrNothing(t *testing.T) {
	staged := func(path string) []writtenFile {
		return []writtenFile{{generated: ai.GeneratedFile{Path: path, Action: "update", Content: ".header{}"}}}
	}
	tests := []struct {
		name         string
		finishFirst  bool // the generation stopped running (cancelled, reaped, re-queued) before the commit
		path         string
		wantErr      error
		wantCommited bool
	}{
		{name: "running generation commits reply, files and success together", path: "components/css/header.css", wantCommited: true},
		{name: "generation no longer running commits nothing", finishFirst: true, path: "components/css/header.css", wantErr: ErrGenerationNotRunning},
		{name: "a failing file write rolls back the reply", path: strings.Repeat("a", 600) + ".css"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := openTestDB(t)
			chatSvc := chat.NewService(chat.NewRepository(conn))
			repo := NewRepository(conn)
			svc := &Service{repo: repo, chats: chatSvc}
			ctx := context.Background()
			tenantID := uint64(time.Now().UnixNano())
			c, err := chatSvc.GetOrCreateChat(ctx, tenantID, ChatType)
			if err != nil {
				t.Fatal(err)
			}
			genID := uuid.NewString()
			if err := repo.StartGeneration(ctx, genID, c.ID, tenantID); err != nil {
				t.Fatal(err)
			}
			if tt.finishFirst {
				if err := repo.EndGeneration(ctx, c.ID, genID, nil); err != nil {
					t.Fatal(err)
				}
			}

			err = svc.commitTurn(ctx, c, genID, "Made the header dark.", &ai.Result{}, chat.ApplyStatusPending, staged(tt.path))
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("commitTurn error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantCommited && err != nil {
				t.Fatalf("commitTurn: %v", err)
			}
			if !tt.wantCommited && err == nil {
				t.Fatal("commitTurn must fail")
			}

			messages, err := chatSvc.ListMessagesForVerifiedChat(ctx, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			draft, err := repo.DraftFiles(ctx, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			g, err := repo.GetGeneration(ctx, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantCommited {
				if len(messages) != 1 || len(draft) != 1 || g.Status != GenerationStatusSucceeded {
					t.Errorf("want reply, draft file and success together; got %d messages, %d draft files, status %s", len(messages), len(draft), g.Status)
				}
				return
			}
			if len(messages) != 0 || len(draft) != 0 {
				t.Errorf("want nothing committed; got %d messages, %d draft files", len(messages), len(draft))
			}
			if !tt.finishFirst && g.Status != GenerationStatusRunning {
				t.Errorf("a rolled-back turn must leave its generation running for EndGeneration to record, got %s", g.Status)
			}
		})
	}
}

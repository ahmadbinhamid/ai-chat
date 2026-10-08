package themebuild

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// A queued turn runs exactly as Auto resolved it at enqueue, including a design route with thinking switched off.
func TestRepository_QueuedGenerationKeepsThinkingOff(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	for _, off := range []bool{true, false} {
		chatID := uuid.NewString()
		if _, err := repo.EnqueueGeneration(ctx, Generation{
			ID: uuid.NewString(), ChatID: chatID, TenantID: 1, Prompt: "make the header dark", ThemeSlug: "test-theme",
			ModelID: "deepseek-flash", Effort: "low", ThinkingOff: off,
		}); err != nil {
			t.Fatal(err)
		}
		g, err := repo.DequeueNext(ctx, chatID)
		if err != nil {
			t.Fatal(err)
		}
		if g.ThinkingOff != off || g.ModelID != "deepseek-flash" || g.Effort != "low" {
			t.Errorf("thinking_off=%v: dequeued %+v", off, g)
		}
	}
}

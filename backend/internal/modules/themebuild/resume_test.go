package themebuild

import (
	"context"
	"strings"
	"testing"
	"time"

	"ai-chat/internal/modules/chat"
)

func latestGeneration(t *testing.T, svc *Service, chatID string) Generation {
	t.Helper()
	g, err := svc.repo.GetGeneration(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func assistantReplies(t *testing.T, chatSvc *chat.Service, chatID string) []chat.Message {
	t.Helper()
	messages, err := chatSvc.ListMessagesForVerifiedChat(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	var out []chat.Message
	for _, m := range messages {
		if m.Role == chat.RoleAssistant {
			out = append(out, m)
		}
	}
	return out
}

// A prompt left queued by a restart resumes only with its own sender's fresh token, and says it resumed.
func TestResume_OnlyTheSendersTokenResumesAQueuedPrompt(t *testing.T) {
	ctx := context.Background()
	oldSvc, _ := newQueueTestService(t)
	oldSvc.gen = &scriptedGenerator{}
	oldSvc.Drain(ctx, 0) // shutting down: the prompt below is recorded and queued, never started
	tenantID := uint64(time.Now().UnixNano())
	sender, colleague := tenantID, tenantID+1
	out, err := oldSvc.Generate(ctx, GenerateInput{TenantID: tenantID, UserID: &sender, Token: "old", ThemeSlug: "theme", Prompt: "make the header dark"})
	if err != nil {
		t.Fatal(err)
	}

	newSvc, chatSvc := newQueueTestService(t) // the restarted process: no tokens
	gen := &scriptedGenerator{}
	newSvc.gen = gen
	newSvc.PrepareResume(ctx)
	if g := latestGeneration(t, newSvc, out.Chat.ID); g.Status != GenerationStatusQueued || g.AwaitingResumeSince == nil {
		t.Fatalf("want the prompt queued and waiting for its sender, got %+v", g)
	}

	newSvc.ResumeForUser(ctx, tenantID, &colleague, "colleague-token")
	time.Sleep(200 * time.Millisecond)
	if gen.callCount() != 0 {
		t.Fatal("another user in the same store must never resume someone else's prompt")
	}
	if g := latestGeneration(t, newSvc, out.Chat.ID); g.Status != GenerationStatusQueued {
		t.Fatalf("want it still queued after the colleague's request, got %s", g.Status)
	}

	newSvc.ResumeForUser(ctx, tenantID, &sender, "fresh-token")
	waitForAssistantReplies(t, chatSvc, tenantID, out.Chat.ID, 1)
	reply := assistantReplies(t, chatSvc, out.Chat.ID)[0]
	if reply.Status != chat.MessageStatusCompleted || !strings.Contains(reply.Content, resumedAfterUpdateNote) {
		t.Errorf("want a completed reply with the resumed note, got %s: %q", reply.Status, reply.Content)
	}
}

// Cut off at the drain limit: re-queued once and resumed after the restart; cut off again, it fails clearly.
func TestResume_CutOffGenerationIsRequeuedOnce(t *testing.T) {
	ctx := context.Background()
	oldSvc, chatSvc := newQueueTestService(t)
	oldGen := &scriptedGenerator{results: []scriptedResult{{delay: 600 * time.Millisecond}}}
	oldSvc.gen = oldGen
	tenantID := uint64(time.Now().UnixNano())
	out, err := oldSvc.Generate(ctx, GenerateInput{TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme", Prompt: "redesign the hero"})
	if err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, oldGen, 1, 5*time.Second)

	if _, stillRunning := oldSvc.Drain(ctx, 50*time.Millisecond); stillRunning != 1 {
		t.Fatalf("want the generation cut off at the limit, got %d still running", stillRunning)
	}
	if requeued, failed := oldSvc.RequeueUnfinished(ctx); requeued != 1 || failed != 0 {
		t.Fatalf("want it re-queued, got %d re-queued, %d failed", requeued, failed)
	}
	if g := latestGeneration(t, oldSvc, out.Chat.ID); g.Status != GenerationStatusQueued || g.ResumeCount != 1 {
		t.Fatalf("want it queued with resume_count 1, got %+v", g)
	}
	// The cut-off goroutine still finishes in this process; its late result must be discarded, not recorded.
	time.Sleep(800 * time.Millisecond)
	if n := len(assistantReplies(t, chatSvc, out.Chat.ID)); n != 0 {
		t.Fatalf("a cut-off generation must not record a reply after being re-queued, got %d", n)
	}

	// Restarted, resumed by its sender, and cut off a second time.
	newSvc, _ := newQueueTestService(t)
	newGen := &scriptedGenerator{results: []scriptedResult{{delay: 600 * time.Millisecond}}}
	newSvc.gen = newGen
	newSvc.PrepareResume(ctx)
	newSvc.ResumeForUser(ctx, tenantID, &tenantID, "fresh")
	waitForCalls(t, newGen, 1, 5*time.Second)
	newSvc.Drain(ctx, 50*time.Millisecond)
	if requeued, failed := newSvc.RequeueUnfinished(ctx); requeued != 0 || failed != 1 {
		t.Fatalf("a second interruption must fail it, got %d re-queued, %d failed", requeued, failed)
	}
	replies := assistantReplies(t, chatSvc, out.Chat.ID)
	if len(replies) != 1 || replies[0].Status != chat.MessageStatusFailed || replies[0].Content != errInterruptedTwice.Error() {
		t.Errorf("want one failed reply saying it was interrupted twice, got %+v", replies)
	}
}

// While its sender is away, neither the drain loop nor the reaper fails it; after the window, the reaper does, clearly.
func TestResume_WaitsTenMinutesThenFailsClearly(t *testing.T) {
	ctx := context.Background()
	oldSvc, _ := newQueueTestService(t)
	oldSvc.gen = &scriptedGenerator{}
	oldSvc.Drain(ctx, 0)
	tenantID := uint64(time.Now().UnixNano())
	out, err := oldSvc.Generate(ctx, GenerateInput{TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme", Prompt: "fix the cart"})
	if err != nil {
		t.Fatal(err)
	}

	newSvc, chatSvc := newQueueTestService(t)
	gen := &scriptedGenerator{}
	newSvc.gen = gen
	newSvc.PrepareResume(ctx)

	newSvc.reapOrphanedQueues(ctx)
	if g := latestGeneration(t, newSvc, out.Chat.ID); g.Status != GenerationStatusQueued || len(assistantReplies(t, chatSvc, out.Chat.ID)) != 0 {
		t.Fatalf("within the window the reaper must leave it queued, got %s", g.Status)
	}
	if _, err := newSvc.startChatQueue(ctx, out.Chat); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if g := latestGeneration(t, newSvc, out.Chat.ID); g.Status != GenerationStatusQueued || gen.callCount() != 0 {
		t.Fatalf("without its sender's token the drain loop must hand it back, got %s, %d calls", g.Status, gen.callCount())
	}

	if _, err := newSvc.repo.db.Exec(`UPDATE generations SET awaiting_resume_since = ? WHERE chat_id = ?`,
		time.Now().UTC().Add(-resumeWindow-time.Minute), out.Chat.ID); err != nil {
		t.Fatal(err)
	}
	newSvc.reapOrphanedQueues(ctx)
	replies := assistantReplies(t, chatSvc, out.Chat.ID)
	if len(replies) != 1 || replies[0].Status != chat.MessageStatusFailed || replies[0].Content != errResumeExpired.Error() {
		t.Errorf("want one failed reply explaining it couldn't continue without them, got %+v", replies)
	}
	if g := latestGeneration(t, newSvc, out.Chat.ID); g.Status != GenerationStatusFailed {
		t.Errorf("want the generation failed, got %s", g.Status)
	}
}

package themebuild

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/genfail"
	"ai-chat/internal/prodhardening"
	"ai-chat/internal/themefs"
)

func TestParentGenerationTimeout_IsTenMinutes(t *testing.T) {
	t.Parallel()
	if generateTimeout() != 10*time.Minute {
		t.Fatalf("generateTimeout=%s want 10m", generateTimeout())
	}
	p := prodhardening.DefaultPolicy()
	if p.GenerationTimeout != 10*time.Minute {
		t.Fatalf("policy=%s want 10m", p.GenerationTimeout)
	}
	if int64(p.GenerationTimeout) != generateTimeoutNanos.Load() {
		t.Fatal("policy/runtime timeout mismatch")
	}
	// Child timeouts must stay strictly under the parent budget.
	if !prodhardening.TimeoutHierarchyOK(p) {
		t.Fatal("timeout hierarchy invalid after 10m parent budget")
	}
	if p.StreamFirstTokenTimeout >= p.GenerationTimeout ||
		p.StreamIdleTimeout >= p.GenerationTimeout ||
		p.WebSocketIOTimeout >= p.GenerationTimeout {
		t.Fatal("child timeout must remain bounded below parent")
	}
}

func TestGenerationWaitNotice_CompoundVsNormal(t *testing.T) {
	t.Parallel()
	normal := generationWaitNotice("update the hero banner color")
	if !strings.Contains(normal, "up to 10 minutes") {
		t.Fatalf("normal notice: %q", normal)
	}
	if strings.Contains(normal, "multi-step") {
		t.Fatalf("simple prompt must not use multi-step copy: %q", normal)
	}
	compound := generationWaitNotice("can you help me to create 2 blog pages")
	if !strings.Contains(compound, "multi-step") || !strings.Contains(compound, "up to 10 minutes") {
		t.Fatalf("compound notice: %q", compound)
	}
}

func TestClassify_AIGenerationTimeout(t *testing.T) {
	t.Parallel()
	c := genfail.Classify(context.DeadlineExceeded)
	if c.Code != genfail.CodeAIGenerationTimeout {
		t.Fatalf("code=%s want AI_GENERATION_TIMEOUT", c.Code)
	}
	if !strings.Contains(c.Message, "10 minutes") {
		t.Fatalf("message=%q", c.Message)
	}
	if strings.Contains(strings.ToLower(c.Message), "something went wrong") {
		t.Fatal("must not use generic message")
	}
	san := ai.SanitizeError(context.DeadlineExceeded)
	if strings.Contains(strings.ToLower(san), "something went wrong") {
		t.Fatalf("sanitize generic: %q", san)
	}
}

func TestCompoundTimeout_PreservesCheckpointsAndClassifies(t *testing.T) {
	t.Parallel()
	progress := CompoundProgress{
		Completed: []CompoundStep{{Label: "Create page 1 of 2"}},
		Failed:    &CompoundStep{Label: "Create page 2 of 2"},
		Accum: &ai.Result{Files: []ai.GeneratedFile{
			{Path: "pages/one.liquid", Action: "create", Content: "a"},
		}},
		Registries: []*themefs.PageEntry{{Slug: "one", Page: "one"}},
	}
	msg := CompoundPartialFailureMessage(progress, context.DeadlineExceeded)
	if !strings.Contains(msg, "timed out after 10 minutes") {
		t.Fatalf("msg=%q", msg)
	}
	if !strings.Contains(msg, "Create page 1 of 2") {
		t.Fatalf("missing completed step: %q", msg)
	}
	if !strings.Contains(msg, "remaining steps were not completed") {
		t.Fatalf("missing remaining wording: %q", msg)
	}
	wrapped := &errCompoundPartial{Msg: msg, Cause: context.DeadlineExceeded, Accum: progress.Accum}
	c := genfail.Classify(wrapped)
	if c.Code != genfail.CodeAIGenerationTimeout {
		t.Fatalf("code=%s want AI_GENERATION_TIMEOUT", c.Code)
	}
	if strings.Contains(strings.ToLower(c.Message), "something went wrong") {
		t.Fatalf("generic: %q", c.Message)
	}
}

func TestRepairRespectsParentDeadline(t *testing.T) {
	t.Parallel()
	// shouldStartRepairGenerate must refuse when parent ctx is already dead.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ok, reason := shouldStartRepairGenerate(ctx, 1, maxThemeCheckRetries, 1)
	if ok {
		t.Fatal("repair must not start after parent cancel/deadline")
	}
	if reason != RepairSkipContextCanceled && reason != RepairSkipDeadlineExceeded {
		// canceled ctx reports Canceled
		if reason != RepairSkipContextCanceled {
			t.Fatalf("reason=%s", reason)
		}
	}

	dctx, dcancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer dcancel()
	time.Sleep(time.Millisecond)
	ok, reason = shouldStartRepairGenerate(dctx, 1, maxThemeCheckRetries, 1)
	if ok {
		t.Fatal("repair must not start after parent deadline")
	}
	if reason != RepairSkipDeadlineExceeded && reason != RepairSkipContextCanceled {
		t.Fatalf("reason=%s", reason)
	}
}

func TestChildCannotExceedParentDeadline(t *testing.T) {
	t.Parallel()
	parent, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	// Simulate a child that would want a longer budget — it still inherits parent.
	child, childCancel := context.WithTimeout(parent, 5*time.Minute)
	defer childCancel()
	select {
	case <-child.Done():
		if !errors.Is(child.Err(), context.DeadlineExceeded) && !errors.Is(child.Err(), context.Canceled) {
			t.Fatalf("child err=%v", child.Err())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("child outlived parent deadline")
	}
}

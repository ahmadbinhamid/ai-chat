package themebuild

import (
	"context"
	"testing"
	"time"
)

func TestDrain_WaitsForRunningGenerations(t *testing.T) {
	tests := []struct {
		name             string
		runFor           time.Duration
		limit            time.Duration
		wantFinished     int
		wantStillRunning int
	}{
		{name: "finishes within the limit", runFor: 150 * time.Millisecond, limit: 2 * time.Second, wantFinished: 1},
		{name: "cut off at the limit", runFor: 2 * time.Second, limit: 150 * time.Millisecond, wantStillRunning: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &Service{}
			if !svc.runs.start() {
				t.Fatal("a loop must start before draining")
			}
			go func() {
				time.Sleep(tt.runFor)
				svc.runs.done()
			}()

			finished, stillRunning := svc.Drain(context.Background(), tt.limit)
			if finished != tt.wantFinished || stillRunning != tt.wantStillRunning {
				t.Errorf("Drain = %d finished, %d still running; want %d, %d", finished, stillRunning, tt.wantFinished, tt.wantStillRunning)
			}
			if svc.runs.start() {
				t.Error("no generation may start once draining has begun")
			}
		})
	}
}

func TestDrain_NothingRunningReturnsAtOnce(t *testing.T) {
	svc := &Service{}
	start := time.Now()
	if finished, stillRunning := svc.Drain(context.Background(), time.Minute); finished != 0 || stillRunning != 0 || time.Since(start) > time.Second {
		t.Errorf("an idle drain must return at once with nothing counted, got %d/%d after %s", finished, stillRunning, time.Since(start))
	}
}

// A prompt sent while draining is recorded and queued, never started.
func TestGenerate_WhileDrainingLeavesThePromptQueued(t *testing.T) {
	svc, _ := newQueueTestService(t)
	gen := &scriptedGenerator{}
	svc.gen = gen
	svc.Drain(context.Background(), 0)

	tenantID := uint64(time.Now().UnixNano())
	out, err := svc.Generate(context.Background(), GenerateInput{TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme", Prompt: "late"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if gen.callCount() != 0 {
		t.Errorf("no generation may run while draining, got %d calls", gen.callCount())
	}
	pending, err := svc.ListPendingGenerations(context.Background(), out.Chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Status != GenerationStatusQueued {
		t.Errorf("want the prompt left queued, got %+v", pending)
	}
}

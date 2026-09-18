package themebuild

import (
	"context"
	"testing"
	"time"

	"ai-chat/internal/genlifecycle"

	"github.com/google/uuid"
)

func TestDeliverToSubscriber_TerminalForcesRoom(t *testing.T) {
	ch := make(chan GenerationEvent, 1)
	ch <- GenerationEvent{Type: EventTypeStarted, GenerationID: "g1"}

	before := genlifecycle.ReadSnapshot().TerminalBusForcedDeliveries
	deliverToSubscriber(ch, "chat-1", GenerationEvent{
		Type: EventTypeDone, GenerationID: "g1", Seq: 9,
	})
	select {
	case ev := <-ch:
		if ev.Type != EventTypeDone {
			t.Fatalf("got type %q want done", ev.Type)
		}
	default:
		t.Fatal("expected terminal event in channel")
	}
	if genlifecycle.ReadSnapshot().TerminalBusForcedDeliveries <= before {
		t.Fatal("expected TerminalBusForcedDeliveries to increment")
	}
}

func TestDeliverToSubscriber_NonTerminalDropsWhenFull(t *testing.T) {
	ch := make(chan GenerationEvent, 1)
	ch <- GenerationEvent{Type: EventTypeStarted, GenerationID: "g1"}
	deliverToSubscriber(ch, "chat-1", GenerationEvent{
		Type: EventTypeChecking, GenerationID: "g1",
	})
	select {
	case ev := <-ch:
		if ev.Type != EventTypeStarted {
			t.Fatalf("buffered event should remain started, got %q", ev.Type)
		}
	default:
		t.Fatal("channel should still hold the original event")
	}
}

func TestInProcessEventBus_PublishDoesNotBlockOnSlowConsumer(t *testing.T) {
	bus := newInProcessEventBus()
	ch, cancel := bus.Subscribe(context.Background(), "chat-slow")
	defer cancel()

	// Fill the buffer.
	for i := 0; i < subscriberBufferSize; i++ {
		bus.Publish(context.Background(), "chat-slow", GenerationEvent{
			Type: EventTypeThinking, GenerationID: "g1", Seq: 0,
		})
	}
	done := make(chan struct{})
	go func() {
		bus.Publish(context.Background(), "chat-slow", GenerationEvent{
			Type: EventTypeDone, GenerationID: "g1", Seq: 1,
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on full subscriber")
	}
	// Drain until we see done (terminal forced in).
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == EventTypeDone {
				return
			}
		case <-deadline:
			t.Fatal("did not receive terminal done from slow subscriber path")
		}
	}
}

func TestEventEmitter_DuplicateTerminalSuppressed(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()
	genID := seedGeneration(t, repo, chatID)

	emitter := newEventEmitter(ctx, repo, nil, genID, chatID)
	emitter.emit(ctx, EventTypeStarted, map[string]any{})
	emitter.emit(ctx, EventTypeDone, map[string]string{"summary": "ok"})
	emitter.emit(ctx, EventTypeFailed, map[string]string{"message": "should not persist"})
	emitter.emit(ctx, EventTypeChecking, map[string]int{"attempt": 1})

	events, err := repo.GetEventsSince(ctx, chatID, 0)
	if err != nil {
		t.Fatalf("GetEventsSince: %v", err)
	}
	terminals := 0
	for _, ev := range events {
		if genlifecycle.IsTerminal(ev.Type) {
			terminals++
			if ev.Type != EventTypeDone {
				t.Fatalf("unexpected terminal %q", ev.Type)
			}
		}
		if ev.Type == EventTypeChecking {
			t.Fatal("checking after done must be suppressed")
		}
	}
	if terminals != 1 {
		t.Fatalf("terminals=%d want 1 (events=%+v)", terminals, events)
	}
}

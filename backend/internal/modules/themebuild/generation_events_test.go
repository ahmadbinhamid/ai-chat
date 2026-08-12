package themebuild

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// openTestRedis connects to the same Redis this repo's .env already points
// at (REDIS_URL, defaulting to localhost) and skips the test if it isn't
// reachable — mirrors openTestDB's approach for MySQL.
func openTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	rdb, err := NewRedisClient(getenv("REDIS_URL", "redis://127.0.0.1:6379"))
	if err != nil {
		t.Fatalf("invalid test REDIS_URL: %v", err)
	}
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		_ = rdb.Close()
		t.Skipf("skipping: test redis not reachable: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func seedGeneration(t *testing.T, repo *Repository, chatID string) string {
	t.Helper()
	genID := uuid.NewString()
	if err := repo.StartGeneration(context.Background(), genID, chatID, 1); err != nil {
		t.Fatalf("failed to seed a generation row: %v", err)
	}
	return genID
}

func TestGenerationEvents_AppendAndGetSince(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()
	genID := seedGeneration(t, repo, chatID)

	emitter := newEventEmitter(ctx, repo, nil, genID, chatID)
	emitter.emit(ctx, EventTypeStarted, struct{}{})
	emitter.emit(ctx, EventTypeChecking, map[string]int{"attempt": 1})
	emitter.emit(ctx, EventTypeDone, map[string]string{"summary": "ok"})

	events, err := repo.GetEventsSince(ctx, chatID, 0)
	if err != nil {
		t.Fatalf("GetEventsSince failed: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d: %+v", len(events), events)
	}
	if events[0].Type != EventTypeStarted || events[0].Seq != 1 {
		t.Errorf("unexpected first event: %+v", events[0])
	}
	if events[2].Type != EventTypeDone || events[2].Seq != 3 {
		t.Errorf("unexpected last event: %+v", events[2])
	}

	// Replaying "since seq 1" should skip the first event.
	since, err := repo.GetEventsSince(ctx, chatID, 1)
	if err != nil {
		t.Fatalf("GetEventsSince failed: %v", err)
	}
	if len(since) != 2 || since[0].Type != EventTypeChecking {
		t.Fatalf("expected 2 events starting at checking, got %+v", since)
	}
}

func TestGenerationEvents_RetentionTrimsToLast200PerChat(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()
	genID := seedGeneration(t, repo, chatID)

	emitter := newEventEmitter(ctx, repo, nil, genID, chatID)
	for i := 0; i < maxGenerationEventsPerChat+10; i++ {
		emitter.emit(ctx, EventTypeChecking, map[string]int{"attempt": i})
	}

	var count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM generation_events WHERE chat_id = ?`, chatID).Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != maxGenerationEventsPerChat {
		t.Errorf("expected exactly %d events retained, got %d", maxGenerationEventsPerChat, count)
	}

	// The retained rows must be the most recent ones, not an arbitrary 200.
	events, err := repo.GetEventsSince(ctx, chatID, 0)
	if err != nil {
		t.Fatalf("GetEventsSince failed: %v", err)
	}
	if len(events) != maxGenerationEventsPerChat {
		t.Fatalf("expected %d events for this generation, got %d", maxGenerationEventsPerChat, len(events))
	}
	if events[0].Seq != 11 {
		t.Errorf("expected the oldest retained event to be seq 11 (10 trimmed), got seq %d", events[0].Seq)
	}
}

// TestNewEventEmitter_SeqContinuesAcrossGenerationsOnSameChat is the
// regression test for the bug where every new generation on a chat
// restarted its seq at 1, colliding with the previous generation's seq
// numbers — see newEventEmitter's doc comment. Two sequential generations
// on the same chat must produce strictly increasing, non-colliding seq
// values (1,2 then 3,4 — not 1,2 again).
func TestNewEventEmitter_SeqContinuesAcrossGenerationsOnSameChat(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()

	gen1 := seedGeneration(t, repo, chatID)
	emitter1 := newEventEmitter(ctx, repo, nil, gen1, chatID)
	emitter1.emit(ctx, EventTypeStarted, struct{}{})
	emitter1.emit(ctx, EventTypeDone, map[string]string{"summary": "ok"})

	// StartGeneration only allows one in-flight generation per chat (see
	// ErrGenerationInProgress) — end the first before seeding the second.
	if err := repo.EndGeneration(ctx, chatID, nil); err != nil {
		t.Fatalf("failed to end the first generation: %v", err)
	}
	gen2 := seedGeneration(t, repo, chatID)
	emitter2 := newEventEmitter(ctx, repo, nil, gen2, chatID)
	emitter2.emit(ctx, EventTypeStarted, struct{}{})
	emitter2.emit(ctx, EventTypeDone, map[string]string{"summary": "ok again"})

	events, err := repo.GetEventsSince(ctx, chatID, 0)
	if err != nil {
		t.Fatalf("GetEventsSince failed: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("expected 4 events across both generations, got %d: %+v", len(events), events)
	}
	wantSeqs := []int64{1, 2, 3, 4}
	for i, ev := range events {
		if ev.Seq != wantSeqs[i] {
			t.Errorf("event %d: expected seq %d, got %d (generation_id=%s)", i, wantSeqs[i], ev.Seq, ev.GenerationID)
		}
	}
	if events[2].GenerationID != gen2 || events[2].Seq != 3 {
		t.Errorf("expected the second generation's first event at seq 3, got %+v", events[2])
	}
}

func TestEventEmitter_PublishesToRedis(t *testing.T) {
	conn := openTestDB(t)
	rdb := openTestRedis(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()
	genID := seedGeneration(t, repo, chatID)

	sub := rdb.Subscribe(ctx, redisChannelForChat(chatID))
	defer func() { _ = sub.Close() }()
	// Ensure the subscription is actually registered with Redis before we
	// publish — Subscribe returns before the SUBSCRIBE command round-trips.
	if _, err := sub.Receive(ctx); err != nil {
		t.Fatalf("subscribe failed: %v", err)
	}

	emitter := newEventEmitter(ctx, repo, newRedisEventBus(rdb), genID, chatID)
	emitter.emit(ctx, EventTypeDone, map[string]string{"summary": "published"})

	select {
	case msg := <-sub.Channel():
		var got GenerationEvent
		if err := json.Unmarshal([]byte(msg.Payload), &got); err != nil {
			t.Fatalf("failed to decode published event: %v", err)
		}
		if got.Type != EventTypeDone || got.GenerationID != genID {
			t.Errorf("unexpected published event: %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the published event")
	}
}

func TestNewRedisClient_EmptyURLReturnsNilNoError(t *testing.T) {
	rdb, err := NewRedisClient("")
	if err != nil || rdb != nil {
		t.Errorf("expected (nil, nil) for an empty REDIS_URL, got (%v, %v)", rdb, err)
	}
}

func TestNewRedisClient_InvalidURLReturnsError(t *testing.T) {
	if _, err := NewRedisClient("not-a-valid-url"); err == nil {
		t.Error("expected an error for a malformed REDIS_URL")
	}
}

func TestEventEmitter_NilRepoIsANoOp(t *testing.T) {
	var emitter *eventEmitter
	// Must not panic — this is the "test constructed a bare &Service{}"
	// convenience checkAndRepair's tests already rely on.
	emitter.emit(context.Background(), EventTypeStarted, struct{}{})

	emitter2 := newEventEmitter(context.Background(), nil, nil, "gen", "chat")
	emitter2.emit(context.Background(), EventTypeStarted, struct{}{})
}

// TestEmitLive_PublishesButNeverPersistsOrAdvancesSeq is the regression
// test for emitLive's whole reason to exist (see its doc comment): a
// high-frequency ephemeral event (streamed model text) must reach a live
// subscriber over the bus, but must never hit generation_events (an insert
// plus a trim DELETE per call — see AppendGenerationEvent) and must never
// consume a seq number, since seq is the replay watermark every durable
// event on this chat shares (see eventEmitter's doc comment) — a "thinking"
// event stealing one would desync GetEventsSince's replay window for
// everything else.
func TestEmitLive_PublishesButNeverPersistsOrAdvancesSeq(t *testing.T) {
	conn := openTestDB(t)
	rdb := openTestRedis(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()
	genID := seedGeneration(t, repo, chatID)

	sub := rdb.Subscribe(ctx, redisChannelForChat(chatID))
	defer func() { _ = sub.Close() }()
	if _, err := sub.Receive(ctx); err != nil {
		t.Fatalf("subscribe failed: %v", err)
	}

	emitter := newEventEmitter(ctx, repo, newRedisEventBus(rdb), genID, chatID)
	seqBefore := emitter.nextSeq

	emitter.emitLive(ctx, EventTypeThinking, map[string]string{"text": "hello"})

	select {
	case msg := <-sub.Channel():
		var got GenerationEvent
		if err := json.Unmarshal([]byte(msg.Payload), &got); err != nil {
			t.Fatalf("failed to decode published event: %v", err)
		}
		if got.Type != EventTypeThinking || got.Seq != 0 {
			t.Errorf("expected a live Seq:0 thinking event, got %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the live-published event")
	}

	if emitter.nextSeq != seqBefore {
		t.Errorf("expected emitLive to leave nextSeq unchanged, was %d now %d", seqBefore, emitter.nextSeq)
	}

	events, err := repo.GetEventsSince(ctx, chatID, 0)
	if err != nil {
		t.Fatalf("GetEventsSince failed: %v", err)
	}
	for _, ev := range events {
		if ev.Type == EventTypeThinking {
			t.Errorf("expected emitLive to never persist to generation_events, found %+v", ev)
		}
	}
}

// TestEmit_StillPersistsAndAdvancesSeq is emitLive's test's counterpart —
// the durable path (emit) must still do both of the things emitLive
// deliberately skips.
func TestEmit_StillPersistsAndAdvancesSeq(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()
	genID := seedGeneration(t, repo, chatID)

	emitter := newEventEmitter(ctx, repo, nil, genID, chatID)
	seqBefore := emitter.nextSeq

	emitter.emit(ctx, EventTypeChecking, map[string]int{"attempt": 1})

	if emitter.nextSeq != seqBefore+1 {
		t.Errorf("expected nextSeq to advance by exactly 1, was %d now %d", seqBefore, emitter.nextSeq)
	}

	events, err := repo.GetEventsSince(ctx, chatID, 0)
	if err != nil {
		t.Fatalf("GetEventsSince failed: %v", err)
	}
	if len(events) != 1 || events[0].Type != EventTypeChecking || events[0].Seq != seqBefore {
		t.Fatalf("expected the checking event durably persisted at seq %d, got %+v", seqBefore, events)
	}
}

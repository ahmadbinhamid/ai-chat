package themebuild

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"ai-chat/internal/safego"

	"github.com/redis/go-redis/v9"
)

// eventBus is the live (non-durable) fan-out path for a generation's
// progress events — generation_events (see generation_events.go) is always
// the system of record; this is only ever the fast path a connected
// WebSocket rides. Two implementations: redisEventBus (cross-replica, used
// whenever REDIS_URL is configured) and inProcessEventBus (same-process
// only, the fallback when it isn't — see NewService). Either way, Stream
// must never treat "no live bus" as a reason to close the connection: a
// client that misses a live event still catches up via EventsSince on
// (re)connect.
type eventBus interface {
	// Publish best-effort delivers ev to every current subscriber of
	// chatID — never blocks the caller waiting on a slow/absent
	// subscriber, and never returns an error the generation itself should
	// care about (durability is generation_events' job, not this one's).
	Publish(ctx context.Context, chatID string, ev GenerationEvent)
	// Subscribe returns a channel of chatID's live events plus a cancel
	// func the caller must call exactly once when done (releases the
	// subscription; does not close ch itself to the caller — reading from
	// ch after cancel simply yields no more events).
	Subscribe(ctx context.Context, chatID string) (ch <-chan GenerationEvent, cancel func())
}

// subscriberBufferSize is how many undelivered events a single subscriber
// channel holds before Publish starts dropping for it — generous relative
// to maxGenerationEventsPerChat's "keep the last 200", since one connection
// falling behind by more than this within one generation's lifetime is
// already well past the point where EventsSince on reconnect is the right
// recovery path anyway.
const subscriberBufferSize = 32

// inProcessEventBus fans events out to every subscriber currently held in
// this process's memory — the fallback when REDIS_URL isn't set (see
// NewService). A subscriber on a different replica than the one publishing
// simply never sees anything from this bus; that's the known, accepted
// limitation of running without Redis on more than one replica (see
// config.Config.RedisURL's doc comment), not a bug in this type.
type inProcessEventBus struct {
	mu   sync.Mutex
	subs map[string][]chan GenerationEvent
}

func newInProcessEventBus() *inProcessEventBus {
	return &inProcessEventBus{subs: make(map[string][]chan GenerationEvent)}
}

func (b *inProcessEventBus) Publish(_ context.Context, chatID string, ev GenerationEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs[chatID] {
		select {
		case ch <- ev:
		default:
			// Slow consumer: drop rather than block emit — see
			// subscriberBufferSize's doc comment.
			slog.Warn("in-process event bus: dropped event, subscriber buffer full", "chat_id", chatID, "type", ev.Type)
		}
	}
}

func (b *inProcessEventBus) Subscribe(_ context.Context, chatID string) (<-chan GenerationEvent, func()) {
	ch := make(chan GenerationEvent, subscriberBufferSize)
	b.mu.Lock()
	b.subs[chatID] = append(b.subs[chatID], ch)
	b.mu.Unlock()

	cancel := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		subs := b.subs[chatID]
		for i, c := range subs {
			if c == ch {
				b.subs[chatID] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		if len(b.subs[chatID]) == 0 {
			delete(b.subs, chatID)
		}
	}
	return ch, cancel
}

// redisEventBus is the cross-replica implementation, backed by the same
// redis.Client eventEmitter used to publish directly before this type
// existed — same wire format (JSON-encoded GenerationEvent), same channel
// naming (redisChannelForChat), so a redisEventBus subscriber and a raw
// rdb.Subscribe caller (e.g. an existing test) are wire-compatible.
type redisEventBus struct {
	rdb *redis.Client
}

func newRedisEventBus(rdb *redis.Client) *redisEventBus {
	return &redisEventBus{rdb: rdb}
}

func (b *redisEventBus) Publish(ctx context.Context, chatID string, ev GenerationEvent) {
	encoded, err := json.Marshal(ev)
	if err != nil {
		slog.Error("redis event bus: failed to marshal event", "chat_id", chatID, "error", err)
		return
	}
	if err := b.rdb.Publish(ctx, redisChannelForChat(chatID), encoded).Err(); err != nil {
		slog.Warn("redis event bus: failed to publish", "chat_id", chatID, "error", err)
	}
}

// Subscribe opens a Redis PubSub and relays decoded messages to ch in a
// background goroutine until cancel is called (which closes the PubSub,
// ending that goroutine) — the caller only ever sees GenerationEvent, never
// the underlying *redis.PubSub or its raw JSON payload.
func (b *redisEventBus) Subscribe(ctx context.Context, chatID string) (<-chan GenerationEvent, func()) {
	sub := b.rdb.Subscribe(ctx, redisChannelForChat(chatID))
	ch := make(chan GenerationEvent, subscriberBufferSize)

	go func() {
		defer close(ch)
		for msg := range sub.Channel() {
			// Wrapped per-message (not once for the whole relay): this
			// goroutine is meant to keep running for the subscription's
			// entire lifetime, so one bad message recovering shouldn't end
			// delivery for every message after it too.
			func() {
				defer safego.Recover("themebuild.eventBusSubscribe")
				var ev GenerationEvent
				if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
					slog.Error("redis event bus: failed to decode published event", "chat_id", chatID, "error", err)
					return
				}
				select {
				case ch <- ev:
				default:
					slog.Warn("redis event bus: dropped event, subscriber buffer full", "chat_id", chatID, "type", ev.Type)
				}
			}()
		}
	}()

	cancel := func() { _ = sub.Close() }
	return ch, cancel
}

package themebuild

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"ai-chat/internal/safego"

	"github.com/redis/go-redis/v9"
)

// Live fan-out; generation_events is system of record, so missed events are fine (catch up via EventsSince).
type eventBus interface {
	Publish(ctx context.Context, chatID string, ev GenerationEvent)
	Subscribe(ctx context.Context, chatID string) (ch <-chan GenerationEvent, cancel func())
}

const subscriberBufferSize = 32

// In-process only; cross-replica isolation is an accepted limitation without Redis.
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
			// Slow consumer: drop rather than block emit.
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

// Cross-replica implementation using rdb.Subscribe.
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

func (b *redisEventBus) Subscribe(ctx context.Context, chatID string) (<-chan GenerationEvent, func()) {
	sub := b.rdb.Subscribe(ctx, redisChannelForChat(chatID))
	ch := make(chan GenerationEvent, subscriberBufferSize)

	go func() {
		defer close(ch)
		for msg := range sub.Channel() {
			// Per-message recovery: one bad message doesn't kill delivery.
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

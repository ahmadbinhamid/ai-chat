package themebuild

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// GenerationEvent is one entry in a generation's durable progress log —
// what a WebSocket client replays on (re)connect and what Redis publishes
// live to any replica other than the one running the generation.
type GenerationEvent struct {
	ID           string
	GenerationID string
	ChatID       string
	Seq          int64
	Type         string
	Payload      json.RawMessage
	CreatedAt    time.Time
}

// Event type constants — see docs/AI_CHAT_IMPLEMENTATION_BRIEF.md phase 3b.
const (
	EventTypeStarted     = "started"
	EventTypeChecking    = "checking"
	EventTypeCheckFailed = "check_failed"
	EventTypeRepairing   = "repairing"
	EventTypeDone        = "done"
	EventTypeFailed      = "failed"
)

// maxGenerationEventsPerChat is "keep the last 200 per chat" — trimmed
// after every insert. Generations emit a handful of events each, so this
// stays cheap in practice; correctness (never growing unbounded) matters
// more here than shaving one query off the common case.
const maxGenerationEventsPerChat = 200

// AppendGenerationEvent inserts one event, then trims chatID's log back to
// the most recent maxGenerationEventsPerChat rows.
func (r *Repository) AppendGenerationEvent(ctx context.Context, ev GenerationEvent) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO generation_events (id, generation_id, chat_id, seq, type, payload, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, ev.ID, ev.GenerationID, ev.ChatID, ev.Seq, ev.Type, []byte(ev.Payload), ev.CreatedAt)
	if err != nil {
		return err
	}

	_, err = r.db.ExecContext(ctx, `
		DELETE FROM generation_events
		WHERE chat_id = ? AND id NOT IN (
			SELECT id FROM (
				SELECT id FROM generation_events WHERE chat_id = ? ORDER BY seq DESC LIMIT ?
			) AS keep
		)
	`, ev.ChatID, ev.ChatID, maxGenerationEventsPerChat)
	return err
}

// GetEventsSince returns generationID's events with seq > sinceSeq, in
// order — what a WebSocket client passing {"last_seq": N} on connect gets
// replayed before it subscribes to the live Redis channel.
func (r *Repository) GetEventsSince(ctx context.Context, generationID string, sinceSeq int64) ([]GenerationEvent, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, generation_id, chat_id, seq, type, payload, created_at
		FROM generation_events WHERE generation_id = ? AND seq > ? ORDER BY seq ASC
	`, generationID, sinceSeq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var events []GenerationEvent
	for rows.Next() {
		var ev GenerationEvent
		var payload sql.NullString
		if err := rows.Scan(&ev.ID, &ev.GenerationID, &ev.ChatID, &ev.Seq, &ev.Type, &payload, &ev.CreatedAt); err != nil {
			return nil, err
		}
		if payload.Valid {
			ev.Payload = json.RawMessage(payload.String)
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}

// redisChannelForChat is the Redis pub/sub channel a generation's events
// are published to — see docs/AI_CHAT_IMPLEMENTATION_BRIEF.md phase 3b:
// "gen:{chat_id}".
func redisChannelForChat(chatID string) string { return "gen:" + chatID }

// eventEmitter emits one generation's progress events: durably to
// generation_events (always) and live to Redis (best-effort, only if a
// client was configured — see Service.redis). A publish failure is logged
// and otherwise ignored — Redis is the live-delivery fast path, never the
// system of record; a WebSocket that missed a live update still catches up
// via GetEventsSince on (re)connect.
type eventEmitter struct {
	repo         *Repository
	redis        *redis.Client
	generationID string
	chatID       string
	nextSeq      int64
}

func newEventEmitter(repo *Repository, rdb *redis.Client, generationID, chatID string) *eventEmitter {
	return &eventEmitter{repo: repo, redis: rdb, generationID: generationID, chatID: chatID, nextSeq: 1}
}

// emit is a no-op (never blocks the generation, never fails it) if repo is
// nil — the same test-construction convenience already used for attempts
// tracking (see checkAndRepair).
func (e *eventEmitter) emit(ctx context.Context, eventType string, payload any) {
	if e == nil || e.repo == nil {
		return
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		slog.Error("failed to marshal generation event payload", "type", eventType, "error", err)
		return
	}

	ev := GenerationEvent{
		ID: uuid.NewString(), GenerationID: e.generationID, ChatID: e.chatID,
		Seq: e.nextSeq, Type: eventType, Payload: payloadJSON, CreatedAt: time.Now().UTC(),
	}
	e.nextSeq++

	if err := e.repo.AppendGenerationEvent(ctx, ev); err != nil {
		slog.Error("failed to persist generation event", "type", eventType, "chat_id", e.chatID, "error", err)
	}

	if e.redis == nil {
		return
	}
	encoded, err := json.Marshal(ev)
	if err != nil {
		return
	}
	if err := e.redis.Publish(ctx, redisChannelForChat(e.chatID), encoded).Err(); err != nil {
		slog.Warn("failed to publish generation event to redis", "chat_id", e.chatID, "error", err)
	}
}

// NewRedisClient parses redisURL (e.g. "redis://127.0.0.1:6379") into a
// client, or returns (nil, nil) if redisURL is empty — the caller (server
// wiring) treats a nil client as "Redis not configured", not an error; see
// config.Config.RedisURL's doc comment on why that's a degrade, not a
// startup failure. A non-empty but malformed URL IS a startup error —
// that's a config mistake, not an absent-Redis deployment.
func NewRedisClient(redisURL string) (*redis.Client, error) {
	if redisURL == "" {
		return nil, nil
	}
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, errors.New("invalid REDIS_URL: " + err.Error())
	}
	return redis.NewClient(opts), nil
}

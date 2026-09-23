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

// GenerationEvent is one entry in a generation's durable progress log, replayed on reconnect
// and published live to other replicas via Redis.
type GenerationEvent struct {
	ID           string
	GenerationID string
	ChatID       string
	Seq          int64
	Type         string
	Payload      json.RawMessage
	CreatedAt    time.Time
}

const (
	EventTypeStarted     = "started"
	EventTypeChecking    = "checking"
	EventTypeCheckFailed = "check_failed"
	EventTypeRepairing   = "repairing"
	EventTypeDone        = "done"
	EventTypeFailed      = "failed"
	// EventTypeQueued: a new prompt lost the race to claim the running slot. Payload: {position, prompt_preview}.
	EventTypeQueued = "queued"
	// EventTypeDequeued: this generation is now running. No payload; the client already has the
	// prompt from the earlier "queued" event for this generation_id.
	EventTypeDequeued = "dequeued"
	// EventTypeCancelled: a generation stopped due to a cancel request, whether it was queued or
	// running. Payload: {"generation_id": "..."}.
	EventTypeCancelled = "cancelled"
	// EventTypeCancelRequested is live-only internal plumbing asking the owning replica to stop;
	// stream.go filters it from what's relayed to the client. Payload: {"generation_id": "..."}.
	EventTypeCancelRequested = "cancel_requested"
	// EventTypeToolCall is emitted just before a read-only theme tool runs. Payload:
	// {"tool": "...", "path"/"pattern": "..."}.
	EventTypeToolCall = "tool_call"
	// EventTypeToolResult is emitted just after, even on tool error, so the step list never gets
	// stuck. Payload: {"summary": "..."}.
	EventTypeToolResult = "tool_result"
	// EventTypeProposing fires when the model's propose_changes call returns non-empty. Payload: {"file_count": N}.
	EventTypeProposing = "proposing"
	// EventTypeStaged fires once the write plan is built, before it's committed. Payload: {"paths": [...]}.
	EventTypeStaged = "staged"
	// EventTypeFetchingLink fires right before fetching a URL found in the prompt. Payload: {"url": "..."}.
	EventTypeFetchingLink = "fetching_link"
	// EventTypeFetchedLink fires once the URL's HTML fetched and digested successfully; not
	// emitted on failure. title/stylesheet_count may be empty/0 on a cache hit — not an error.
	EventTypeFetchedLink = "fetched_link"
	// EventTypeThinking is EPHEMERAL — see emitLive. Never pass to emit(): it would durably
	// persist every chunk and burn a seq number per chunk, breaking the replay window.
	EventTypeThinking = "thinking"
)

// maxPromptPreviewChars bounds EventTypeQueued's prompt_preview field; the full prompt is
// already durably stored on the generations row, so this event doesn't need it too.
const maxPromptPreviewChars = 80

// PromptPreview truncates prompt to maxPromptPreviewChars runes, not bytes, to avoid splitting
// a multi-byte character.
func PromptPreview(prompt string) string {
	r := []rune(prompt)
	if len(r) <= maxPromptPreviewChars {
		return prompt
	}
	return string(r[:maxPromptPreviewChars])
}

// maxGenerationEventsPerChat caps this chat's event log, trimmed after every insert — never
// growing unbounded matters more here than shaving one query off the common case.
const maxGenerationEventsPerChat = 200

// AppendGenerationEvent inserts one event, then trims chatID's log back to the most recent
// maxGenerationEventsPerChat rows.
func (r *Repository) AppendGenerationEvent(ctx context.Context, ev GenerationEvent) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO generation_events (id, generation_id, chat_id, seq, type, payload, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, ev.ID, ev.GenerationID, ev.ChatID, ev.Seq, ev.Type, []byte(ev.Payload), ev.CreatedAt, ev.CreatedAt)
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

// GetEventsSince returns chatID's events with seq > sinceSeq, replayed on reconnect before
// subscribing live. Scoped to chat_id, not generation_id, since a reconnect can straddle two generations.
func (r *Repository) GetEventsSince(ctx context.Context, chatID string, sinceSeq int64) ([]GenerationEvent, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, generation_id, chat_id, seq, type, payload, created_at
		FROM generation_events WHERE chat_id = ? AND seq > ? ORDER BY seq ASC
	`, chatID, sinceSeq)
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

// GetMaxSeqForChat returns the highest seq recorded for chatID (0 if none), so newEventEmitter
// can continue the seq counter across generations instead of restarting at 1.
func (r *Repository) GetMaxSeqForChat(ctx context.Context, chatID string) (int64, error) {
	var maxSeq sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
		SELECT MAX(seq) FROM generation_events WHERE chat_id = ?
	`, chatID).Scan(&maxSeq)
	if err != nil {
		return 0, err
	}
	return maxSeq.Int64, nil
}

// redisChannelForChat is the Redis pub/sub channel for a chat's generation events.
func redisChannelForChat(chatID string) string { return "gen:" + chatID }

// eventEmitter emits one generation's progress events: durably to generation_events, and live to
// bus best-effort. seq is monotonic per chat_id, continued from GetMaxSeqForChat, not restarted at 1.
type eventEmitter struct {
	repo         *Repository
	bus          eventBus
	generationID string
	chatID       string
	nextSeq      int64
	// lastHeartbeat is shared by emit/emitLive so both land in the same place instead of double-
	// writing. No mutex needed: both are only ever called from doGenerate's single goroutine per generation.
	lastHeartbeat time.Time
}

// heartbeatThrottle bounds how often emit/emitLive write last_heartbeat_at — a coalesced
// thinking delta can fire several times a second, so this trades precision for write volume.
const heartbeatThrottle = 30 * time.Second

// updateHeartbeatThrottled stamps last_heartbeat_at, skipping if the last write was within
// heartbeatThrottle. No-op if repo is nil (test-construction convenience).
func (e *eventEmitter) updateHeartbeatThrottled(ctx context.Context) {
	if e.repo == nil {
		return
	}
	if !e.lastHeartbeat.IsZero() && time.Since(e.lastHeartbeat) < heartbeatThrottle {
		return
	}
	e.lastHeartbeat = time.Now()
	// Best-effort: never fails or slows down a turn that's otherwise making real progress.
	if err := e.repo.UpdateGenerationHeartbeat(ctx, e.generationID); err != nil {
		slog.Error("failed to update generation heartbeat", "generation_id", e.generationID, "error", err)
	}
}

// newEventEmitter starts nextSeq one past chatID's current max seq. repo may be nil in tests
// that don't care about persistence, in which case the lookup is skipped.
func newEventEmitter(ctx context.Context, repo *Repository, bus eventBus, generationID, chatID string) *eventEmitter {
	var nextSeq int64 = 1
	if repo != nil {
		maxSeq, err := repo.GetMaxSeqForChat(ctx, chatID)
		if err != nil {
			slog.Error("failed to load chat's max event seq, starting from 1", "chat_id", chatID, "error", err)
		} else {
			nextSeq = maxSeq + 1
		}
	}
	return &eventEmitter{repo: repo, bus: bus, generationID: generationID, chatID: chatID, nextSeq: nextSeq}
}

// emit is a no-op if repo is nil (test-construction convenience).
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

	e.updateHeartbeatThrottled(ctx)

	if e.bus != nil {
		e.bus.Publish(ctx, e.chatID, ev)
	}
}

// emitLive publishes to the live bus only, never generation_events, consuming no seq number.
// Always stamps the heartbeat first — thinking deltas are the only signal during a model call, so this keeps it from going stale mid-call.
func (e *eventEmitter) emitLive(ctx context.Context, eventType string, payload any) {
	if e == nil {
		return
	}
	e.updateHeartbeatThrottled(ctx)
	if e.bus == nil {
		return
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		slog.Error("failed to marshal live generation event payload", "type", eventType, "error", err)
		return
	}

	ev := GenerationEvent{
		ID: uuid.NewString(), GenerationID: e.generationID, ChatID: e.chatID,
		Seq: 0, Type: eventType, Payload: payloadJSON, CreatedAt: time.Now().UTC(),
	}
	e.bus.Publish(ctx, e.chatID, ev)
}

// NewRedisClient returns (nil, nil) if redisURL is empty (Redis not configured, a degrade, not
// an error); a non-empty but malformed URL IS a startup error.
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

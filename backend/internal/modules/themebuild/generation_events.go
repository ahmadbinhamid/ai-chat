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
	// EventTypeQueued is emitted once, from Generate, when a new prompt
	// loses the race to claim the running slot — payload:
	// {position, prompt_preview}.
	EventTypeQueued = "queued"
	// EventTypeDequeued is emitted at the top of each drain-loop iteration
	// (see Service.runGeneration) — this generation is now the one running.
	// No payload: a reconnecting client only needs to know *that* it
	// started, everything else (prompt, etc.) it already has from the
	// earlier "queued" event for this same generation_id.
	EventTypeDequeued = "dequeued"
	// EventTypeCancelled is emitted from the cancel handler when a merchant
	// cancels a still-queued prompt. No payload.
	EventTypeCancelled = "cancelled"
	// EventTypeToolCall is emitted just before a read-only theme tool runs —
	// payload: {"tool": "read_theme_file", "path": "pages/home.liquid"} (or
	// "pattern" for grep_theme; list_theme_files carries just {"tool": ...}).
	EventTypeToolCall = "tool_call"
	// EventTypeToolResult is emitted just after — payload: {"summary": "..."},
	// a short (<~40 char) human-readable outcome (line count, match count,
	// file count). Emitted even when the tool errors, so the step list never
	// gets stuck showing a call with no result.
	EventTypeToolResult = "tool_result"
	// EventTypeProposing is emitted from doGenerate the moment the model's
	// propose_changes call comes back with a non-empty proposal — payload:
	// {"file_count": N}.
	EventTypeProposing = "proposing"
	// EventTypeStaged is emitted once the write plan for an accepted
	// proposal is built (before it's committed to the real theme) — payload:
	// {"paths": [...]}.
	EventTypeStaged = "staged"
	// EventTypeThinking is EPHEMERAL — see emitLive. Never pass this to
	// emit(): it would durably persist every streamed text chunk of every
	// generation, and worse, burn a seq number per chunk, breaking
	// GetEventsSince's replay window for every other event type sharing
	// this chat's seq counter (see eventEmitter's doc comment). Payload:
	// {"text": "..."}, one coalesced chunk at a time — see
	// internal/ai.Generate's onDelta coalescing.
	EventTypeThinking = "thinking"
)

// maxPromptPreviewChars bounds EventTypeQueued's prompt_preview field.
// AppendGenerationEvent runs a trim DELETE with a correlated subquery after
// every insert (see its doc comment) — a full prompt in every queued event
// would make that scan heavier for no benefit, since the full prompt is
// already durably stored on the generations row itself (see generation.go).
const maxPromptPreviewChars = 80

// PromptPreview truncates prompt to maxPromptPreviewChars runes (not
// bytes — a prompt can contain multi-byte characters, and slicing by byte
// count risks cutting one in half) for EventTypeQueued's payload.
func PromptPreview(prompt string) string {
	r := []rune(prompt)
	if len(r) <= maxPromptPreviewChars {
		return prompt
	}
	return string(r[:maxPromptPreviewChars])
}

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

// GetEventsSince returns chatID's events with seq > sinceSeq, in order —
// what a WebSocket client passing {"last_seq": N} on connect gets replayed
// before it subscribes to the live Redis channel. Scoped to chat_id, not
// generation_id: seq is chat-wide monotonic (see eventEmitter's doc
// comment), and a client's last_seq is meant to span every generation
// that's ever run on this chat, not just the latest one — a reconnect that
// landed exactly as one generation finished and the next started must still
// see any tail events of the first one it missed, which a generation_id
// filter tied to only the latest generation would silently drop.
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

// GetMaxSeqForChat returns the highest seq ever recorded for chatID (0 if
// none exist yet) — what newEventEmitter uses to continue a chat's seq
// counter across generations instead of restarting it at 1 (see
// eventEmitter's doc comment). Uses idx_generation_events_chat_seq
// (chat_id, seq), so this is an index-only lookup even once retention has
// trimmed most of a chat's history away.
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

// redisChannelForChat is the Redis pub/sub channel a generation's events
// are published to — see docs/AI_CHAT_IMPLEMENTATION_BRIEF.md phase 3b:
// "gen:{chat_id}".
func redisChannelForChat(chatID string) string { return "gen:" + chatID }

// eventEmitter emits one generation's progress events: durably to
// generation_events (always) and live to bus (best-effort — see eventBus's
// doc comment). A publish failure is logged and otherwise ignored — the bus
// is the live-delivery fast path, never the system of record; a WebSocket
// that missed a live update still catches up via GetEventsSince on
// (re)connect.
//
// seq is monotonic per chat_id, not per generation_id: a WebSocket client
// stays connected across multiple generations on one chat and tracks a
// single last_seq for the whole chat (see stream.go's Stream handler), so a
// second generation restarting at seq 1 would collide with the first
// generation's seq numbers and be indistinguishable from events already
// displayed. newEventEmitter therefore continues from the chat's current
// max seq (see Repository.GetMaxSeqForChat) rather than always starting at
// 1. This read-then-use is safe without extra locking because
// StartGeneration already guarantees at most one generation — and so at
// most one eventEmitter — runs per chat at a time (see
// ErrGenerationInProgress).
type eventEmitter struct {
	repo         *Repository
	bus          eventBus
	generationID string
	chatID       string
	nextSeq      int64
}

// newEventEmitter looks up chatID's current max seq (0 if this chat has
// never emitted an event) and starts nextSeq one past it. repo may be nil
// in tests that don't care about persistence (see emit's nil-repo doc
// comment) — GetMaxSeqForChat is skipped in that case since there's
// nothing to look up against.
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

	if e.bus != nil {
		e.bus.Publish(ctx, e.chatID, ev)
	}
}

// emitLive publishes an event to the live bus only — never to
// generation_events, and never consuming a seq number (always published
// with Seq: 0). For high-frequency, disposable progress (streamed model
// text): a client that reconnects mid-generation simply doesn't see what it
// missed, which is correct for narration but would be wrong for anything
// structural. See emit for the durable path, and AppendGenerationEvent's
// doc comment for why volume matters here (an insert plus a trim DELETE per
// call, unaffordable at one call per token).
//
// A no-op if bus is nil (no REDIS_URL and no in-process subscriber — see
// NewService) or if this generation has no live subscriber at all: unlike
// emit, there's no durable fallback to fall back to, so the ephemeral text
// is simply never seen. That's fine — a merchant not actively watching the
// stream never sees "thinking" narration either way.
func (e *eventEmitter) emitLive(ctx context.Context, eventType string, payload any) {
	if e == nil || e.bus == nil {
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

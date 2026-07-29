package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"ai-chat/internal/auth"
	"ai-chat/internal/logging"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/modules/themebuild"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

// StreamHandler serves GET /chats/:chatId/stream — a WebSocket a merchant's
// browser holds open during a generation to watch its progress live,
// instead of (or alongside) polling GET /chat. Server -> client only: the
// client never sends commands over this connection, prompts still go
// through POST /chats/messages (see message.go).
type StreamHandler struct {
	chats   *chat.Service
	builder *themebuild.Service
}

func NewStreamHandler(chats *chat.Service, builder *themebuild.Service) *StreamHandler {
	return &StreamHandler{chats: chats, builder: builder}
}

// pingInterval matches the brief's "ping every 30 seconds" — keeps
// intermediate proxies/load balancers from treating an idle (no events
// yet) connection as dead and severing it.
const pingInterval = 30 * time.Second

// streamEventMessage is the wire shape sent to the client for every event
// — GenerationEvent's fields, just with a lowercase-first JSON shape
// independent of Go struct tag mechanics elsewhere in this codebase (this
// type is deliberately local to this handler, not reused).
type streamEventMessage struct {
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

// Stream verifies the chat belongs to the caller's tenant, upgrades to a
// WebSocket, replays generation_events after the client's last_seq (if
// given), and — if the generation is still running — subscribes to its
// live Redis channel until it finishes or the connection drops. Auth is
// the same bearer token as every other route (see auth.Middleware, mounted
// on this route's group in server.go) — verified before the upgrade, since
// a 401 can't be expressed cleanly on an already-upgraded connection.
//
// last_seq travels as a query parameter (?last_seq=N), not a post-upgrade
// WebSocket message: coder/websocket closes the whole connection the
// moment any context passed to a read/write carries a deadline that fires
// (see setupReadTimeout/setupWriteTimeout) — there's no safe way to give an
// optional post-connect message a bounded wait without either blocking
// forever on a client that never sends one, or tearing down the connection
// when it doesn't. A query parameter sidesteps that entirely: it's known
// before the upgrade even happens, and the connection is genuinely
// server -> client only from byte zero.
func (h *StreamHandler) Stream(c *gin.Context) {
	tenantID := auth.TenantID(c)
	chatID := c.Param("chatId")

	if _, err := h.chats.GetChat(c.Request.Context(), tenantID, chatID); err != nil {
		if errors.Is(err, chat.ErrNotFound) {
			respondErr(c, chat.ErrNotFound)
			return
		}
		respondErr(c, err)
		return
	}

	lastSeq, err := parseLastSeq(c.Query("last_seq"))
	if err != nil {
		respondBindErr(c, errors.New("last_seq must be a non-negative integer"))
		return
	}

	conn, err := websocket.Accept(c.Writer, c.Request, nil)
	if err != nil {
		// Accept has already written the appropriate HTTP error response.
		return
	}
	defer func() { _ = conn.CloseNow() }()

	// This connection is server -> client only from the start: any data
	// message from the client is a protocol violation CloseRead enforces
	// on our behalf (closing with StatusPolicyViolation), and its returned
	// context is canceled the moment the connection ends for any reason —
	// every subsequent operation uses it so this handler returns promptly
	// on disconnect rather than lingering.
	ctx := conn.CloseRead(context.WithoutCancel(c.Request.Context()))

	gen, err := h.builder.LatestGeneration(ctx, chatID)
	if err != nil {
		if errors.Is(err, themebuild.ErrNotFound) {
			closeStream(conn, websocket.StatusNormalClosure, "no generation has been started for this chat yet")
			return
		}
		slog.Error("stream: failed to load latest generation", "chat_id", chatID, "error", err, "request_id", logging.RequestID(c))
		closeStream(conn, websocket.StatusInternalError, "internal error")
		return
	}

	events, err := h.builder.EventsSince(ctx, gen.ID, lastSeq)
	if err != nil {
		slog.Error("stream: failed to load events", "chat_id", chatID, "generation_id", gen.ID, "error", err, "request_id", logging.RequestID(c))
		closeStream(conn, websocket.StatusInternalError, "internal error")
		return
	}
	for _, ev := range events {
		if !writeStreamEvent(ctx, conn, ev) {
			return
		}
	}

	// Nothing more will ever be published for a generation that's already
	// finished — replaying what's already in generation_events is the
	// whole story, so close now rather than idling forever.
	if gen.Status != themebuild.GenerationStatusRunning {
		closeStream(conn, websocket.StatusNormalClosure, "generation already finished")
		return
	}

	sub := h.builder.SubscribeToGenerationEvents(ctx, chatID)
	if sub == nil {
		closeStream(conn, websocket.StatusNormalClosure, "live updates unavailable (redis not configured on this instance)")
		return
	}
	defer func() { _ = sub.Close() }()

	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-pingTicker.C:
			if err := conn.Ping(ctx); err != nil {
				return
			}

		case msg, ok := <-sub.Channel():
			if !ok {
				return
			}
			var ev themebuild.GenerationEvent
			if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
				slog.Error("stream: failed to decode published event", "chat_id", chatID, "error", err)
				continue
			}
			if !writeStreamEvent(ctx, conn, ev) {
				return
			}
			if ev.Type == themebuild.EventTypeDone || ev.Type == themebuild.EventTypeFailed {
				closeStream(conn, websocket.StatusNormalClosure, "generation finished")
				return
			}
		}
	}
}

// parseLastSeq parses the ?last_seq= query value — "" (the common case: a
// fresh connection, not a reconnect) means replay everything, so it parses
// to 0 rather than an error.
func parseLastSeq(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("invalid last_seq")
	}
	return n, nil
}

func writeStreamEvent(ctx context.Context, conn *websocket.Conn, ev themebuild.GenerationEvent) bool {
	encoded, err := json.Marshal(streamEventMessage{Seq: ev.Seq, Type: ev.Type, Payload: ev.Payload, CreatedAt: ev.CreatedAt})
	if err != nil {
		slog.Error("stream: failed to encode event", "error", err)
		return true // skip this one event, the connection itself is still fine
	}
	return conn.Write(ctx, websocket.MessageText, encoded) == nil
}

func closeStream(conn *websocket.Conn, code websocket.StatusCode, reason string) {
	_ = conn.Close(code, reason)
}

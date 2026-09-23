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

// StreamHandler serves GET /chats/:chatId/stream — a server->client-only WebSocket a browser holds open to watch generation progress live.
// Not mounted behind auth.Middleware: a WebSocket constructor can't set an Authorization header, so it authenticates via auth.WebSocketAuth instead.
type StreamHandler struct {
	chats                *chat.Service
	builder              *themebuild.Service
	authClient           *auth.Client
	authCache            auth.Cache
	authCacheTTL         time.Duration
	authNegativeCacheTTL time.Duration
	// corsAllowedOrigins reuses CORS's origin allow-list for the WS handshake's OriginPatterns: websocket.Accept enforces
	// same-origin by default and gin-contrib/cors's headers never apply to a WS upgrade, so without this every cross-origin dashboard gets rejected.
	corsAllowedOrigins []string
}

func NewStreamHandler(chats *chat.Service, builder *themebuild.Service, authClient *auth.Client, authCache auth.Cache, authCacheTTL, authNegativeCacheTTL time.Duration, corsAllowedOrigins []string) *StreamHandler {
	return &StreamHandler{
		chats:                chats,
		builder:              builder,
		authClient:           authClient,
		authCache:            authCache,
		authCacheTTL:         authCacheTTL,
		authNegativeCacheTTL: authNegativeCacheTTL,
		corsAllowedOrigins:   corsAllowedOrigins,
	}
}

// pingInterval keeps intermediate proxies/load balancers from treating an idle connection as dead. A var so tests can shrink it.
var pingInterval = 30 * time.Second

// ioTimeout bounds every write/ping-wait since the ambient ctx only cancels on a confirmed TCP close, which a peer going dark
// (no clean FIN/RST) never triggers — without this, an unresponsive peer wedges this goroutine and leaks its event-bus subscription forever.
var ioTimeout = 10 * time.Second

// streamEventMessage is GenerationEvent's fields in a stable lowercase JSON shape, local to this handler.
type streamEventMessage struct {
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

// streamReadyMessage tells the client replay finished and it's now watching the live tail — sent once, before the wait loop, in every branch.
// Distinct from the WebSocket's own onopen, which fires before replay and says nothing about last_seq.
type streamReadyMessage struct {
	Type    string `json:"type"`
	LastSeq int64  `json:"last_seq"`
}

// Stream verifies chat ownership, upgrades to a WebSocket, replays generation_events after last_seq...
func (h *StreamHandler) Stream(c *gin.Context) {
	identity, matchedSubprotocol, ok := auth.WebSocketAuth(c, h.authClient, h.authCache, h.authCacheTTL, h.authNegativeCacheTTL)
	if !ok {
		// WebSocketAuth has already written the appropriate error response.
		return
	}
	tenantID := identity.TenantID
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

	conn, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{
		OriginPatterns: h.corsAllowedOrigins,
		// Must echo back one of the client's offered subprotocols — coder/websocket's own client doesn't check this, but a real
		// browser treats a 101 response missing it as a failed handshake. matchedSubprotocol is the "bearer.<...>" entry already parsed out.
		Subprotocols: []string{matchedSubprotocol},
	})
	if err != nil {
		// Accept has already written the appropriate HTTP error response.
		return
	}
	defer func() { _ = conn.CloseNow() }()

	// Server->client only from the start: any client data message is a protocol violation CloseRead closes on our behalf.
	// Its returned context cancels the moment the connection ends, so every subsequent op uses it to return promptly on disconnect.
	ctx := conn.CloseRead(context.WithoutCancel(c.Request.Context()))

	// Subscribed BEFORE replay: a live event published between loading the generation row and subscribing would otherwise
	// be missed entirely (a silent gap). Subscribing first turns the worst case into a duplicate instead; watermark below discards those.
	live, cancel := h.builder.SubscribeToGenerationEvents(ctx, chatID)
	defer cancel()

	var watermark int64 // highest seq already written to this connection

	gen, err := h.builder.LatestGeneration(ctx, chatID)
	if err != nil {
		if !errors.Is(err, themebuild.ErrNotFound) {
			slog.Error("stream: failed to load latest generation", "chat_id", chatID, "error", err, "request_id", logging.RequestID(c))
			closeStream(conn, websocket.StatusInternalError, "internal error")
			return
		}
		// No generation has run yet — nothing to replay, but stay open and subscribed so the next prompt streams live without a reconnect.
		if !writeReady(ctx, conn, 0) {
			return
		}
		h.waitForLiveEvents(ctx, conn, live, &watermark)
		return
	}

	events, err := h.builder.EventsSince(ctx, chatID, lastSeq)
	if err != nil {
		slog.Error("stream: failed to load events", "chat_id", chatID, "generation_id", gen.ID, "error", err, "request_id", logging.RequestID(c))
		closeStream(conn, websocket.StatusInternalError, "internal error")
		return
	}
	for _, ev := range events {
		if !writeStreamEvent(ctx, conn, ev) {
			return
		}
		watermark = ev.Seq
	}

	if !writeReady(ctx, conn, watermark) {
		return
	}

	// A finished generation has nothing more coming, but the chat isn't done — stay open for the next prompt.
	// EventTypeDone/EventTypeFailed are informational to the client, never a reason to close the socket here.
	h.waitForLiveEvents(ctx, conn, live, &watermark)
}

// waitForLiveEvents is the steady state after replay: pings periodically, writes live events newer ...
func (h *StreamHandler) waitForLiveEvents(ctx context.Context, conn *websocket.Conn, live <-chan themebuild.GenerationEvent, watermark *int64) {
	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-pingTicker.C:
			pingCtx, cancel := context.WithTimeout(ctx, ioTimeout)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil {
				// Either ctx.Done() already fired (connection gone) or pingCtx's own timeout fired (peer unresponsive) — either way, done.
				return
			}

		case ev, ok := <-live:
			if !ok {
				// The bus closed its side (e.g. a Redis error) — not a normal-close signal; nothing more will ever arrive here.
				return
			}
			if ev.Type == themebuild.EventTypeCancelRequested {
				// Internal-only signal for whichever replica owns this generation to stop it — not for the client, and never durably seq'd.
				continue
			}
			if ev.Seq != 0 {
				if ev.Seq <= *watermark {
					continue // already delivered during replay
				}
			}
			if !writeStreamEvent(ctx, conn, ev) {
				return
			}
			if ev.Seq != 0 {
				*watermark = ev.Seq
			}
		}
	}
}

// parseLastSeq parses ?last_seq=; "" (a fresh connection) means replay everything, so it parses to 0, not an error.
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
	return writeWithTimeout(ctx, conn, encoded)
}

// writeReady sends the {"type":"ready","last_seq":N} frame marking the end of replay.
func writeReady(ctx context.Context, conn *websocket.Conn, lastSeq int64) bool {
	encoded, err := json.Marshal(streamReadyMessage{Type: "ready", LastSeq: lastSeq})
	if err != nil {
		slog.Error("stream: failed to encode ready frame", "error", err)
		return true
	}
	return writeWithTimeout(ctx, conn, encoded)
}

// writeWithTimeout bounds a write with ioTimeout since coder/websocket's Write has no timeout of its own — a peer that
// stops draining its TCP buffer (not the same as disconnecting) would otherwise stall this goroutine indefinitely.
func writeWithTimeout(ctx context.Context, conn *websocket.Conn, encoded []byte) bool {
	writeCtx, cancel := context.WithTimeout(ctx, ioTimeout)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, encoded) == nil
}

func closeStream(conn *websocket.Conn, code websocket.StatusCode, reason string) {
	_ = conn.Close(code, reason)
}

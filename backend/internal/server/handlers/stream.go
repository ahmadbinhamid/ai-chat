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
//
// This route is deliberately NOT mounted behind the shared auth.Middleware
// (see server.go) — a browser's native WebSocket constructor can't set an
// Authorization header, so it authenticates via auth.WebSocketAuth instead
// (bearer token + tenant ID carried as Sec-WebSocket-Protocol subprotocols).
type StreamHandler struct {
	chats                *chat.Service
	builder              *themebuild.Service
	authClient           *auth.Client
	authCache            auth.Cache
	authCacheTTL         time.Duration
	authNegativeCacheTTL time.Duration
	// corsAllowedOrigins is reused as the WebSocket handshake's origin
	// allow-list (see websocket.AcceptOptions.OriginPatterns below) — the
	// same set of dashboard origins CORS already trusts for every other
	// route. websocket.Accept enforces same-origin ONLY by default (see
	// coder/websocket's authenticateOrigin) — gin-contrib/cors's headers
	// never apply to this handshake at all (CORS is a fetch/XHR concept;
	// a WS upgrade doesn't go through a preflight or carry Access-Control-*
	// response headers), so without this, every cross-origin dashboard
	// (the normal case: the dashboard and this API are different origins)
	// gets its handshake rejected before the Stream handler ever runs.
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

// pingInterval matches the brief's "ping every 30 seconds" — keeps
// intermediate proxies/load balancers from treating an idle (no events
// yet) connection as dead and severing it. A var, not a const, so
// stream_test.go can shrink it to exercise the dead-peer path (see
// ioTimeout's doc comment) without a real test waiting 30+ seconds.
var pingInterval = 30 * time.Second

// ioTimeout bounds every individual write and ping-wait on the connection.
// The handler's ambient ctx (from conn.CloseRead) only cancels once the
// socket is confirmed closed at the TCP level, which a peer that goes dark
// without a clean FIN/RST (WiFi drop, sleeping laptop, a NAT silently
// dropping an idle mapping) never triggers — coder/websocket's Ping and
// Write both block on whatever context they're given with no timeout of
// their own, so using the ambient ctx directly would let a single
// unresponsive peer wedge this goroutine (and leak its event-bus
// subscription) indefinitely. Deriving a short-lived timeout from ctx for
// each individual I/O op, instead, means such a peer gets detected and
// the connection torn down within one interval, while a genuinely closed
// connection still short-circuits immediately via ctx.Done() inside the
// select these derived contexts race against. A var for the same test
// reason as pingInterval.
var ioTimeout = 10 * time.Second

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

// streamReadyMessage tells the client replay has finished and it's now
// watching the live tail — sent exactly once per connection, right before
// this handler enters its wait loop, in every branch (no generation yet,
// generation already finished, generation still running). A frontend
// reconnect loop should treat this as its "transport healthy" signal,
// distinct from the WebSocket's own onopen (which fires before replay,
// telling the client nothing about last_seq yet).
type streamReadyMessage struct {
	Type    string `json:"type"`
	LastSeq int64  `json:"last_seq"`
}

// Stream verifies the chat belongs to the caller's tenant, upgrades to a
// WebSocket, subscribes to the chat's live event bus, replays
// generation_events after the client's last_seq (if given), then relays
// live events for as long as the connection stays open — across an idle
// chat with no generation yet, a finished generation, and a running one,
// this never closes on its own; only a client disconnect or a genuine
// internal error ends it (see writeStreamEvent's callers below). Auth is
// the same bearer token every other route uses, just carried differently
// (see auth.WebSocketAuth) — verified before the upgrade, since a 401 can't
// be expressed cleanly on an already-upgraded connection.
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
		// The WebSocket handshake requires the server to echo back exactly
		// one of the subprotocols the client offered in its own
		// Sec-WebSocket-Protocol response header — coder/websocket's own Go
		// client never checks for this, so a test against it alone stays
		// green even with this omitted, but a real browser's WebSocket
		// constructor treats a 101 response missing it as a failed
		// handshake and never fires onopen at all. matchedSubprotocol is
		// the exact "bearer.<...>" entry auth.WebSocketAuth already parsed
		// out of that same header.
		Subprotocols: []string{matchedSubprotocol},
	})
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

	// Subscribed BEFORE replay (not after): if a live event were published
	// in between loading the generation row and subscribing, replay would
	// never see it (it wasn't in generation_events fast enough) and the
	// live channel wouldn't either (subscribed too late) — a silent gap.
	// Subscribing first means the worst case is now a live event arriving
	// during replay that duplicates one already replayed from
	// generation_events; watermark (below) discards those instead.
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
		// No generation has ever run for this chat yet — nothing to replay,
		// but the connection stays open and subscribed: the merchant's very
		// next prompt starts one, and this same connection should see it
		// live rather than requiring a reconnect.
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

	// A generation that's already finished has nothing more coming from
	// THIS generation, but the chat itself isn't done — the merchant can
	// send another prompt at any time, and this connection stays open and
	// subscribed for it rather than forcing a reconnect. EventTypeDone/
	// EventTypeFailed are informational to the client, never a reason for
	// this handler itself to close the socket (see waitForLiveEvents).
	h.waitForLiveEvents(ctx, conn, live, &watermark)
}

// waitForLiveEvents is a connection's steady state after replay: ping every
// pingInterval, write every live event whose Seq is newer than *watermark
// (discarding the overlap-window duplicates described in Stream's doc
// comment), and return only on client disconnect (ctx.Done) or a
// write/ping failure. It deliberately never returns on
// EventTypeDone/EventTypeFailed — see Stream's doc comment.
//
// Seq == 0 is the ephemeral-event marker (see eventEmitter.emitLive) —
// those are never compared against *watermark and never advance it. They
// aren't replay-able (never durably stored), so "already delivered during
// replay" can never apply to one, and if compared against a real watermark
// (which starts at 0 and only grows) every single Seq: 0 event would
// wrongly look "already delivered" and never reach the client at all — the
// exact bug this carve-out exists to avoid.
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
				// Either the connection is genuinely gone (ctx.Done()
				// already fired, caught on the next loop iteration) or the
				// peer is unresponsive and pingCtx's own timeout fired
				// first — either way, nothing more can be delivered on
				// this connection.
				return
			}

		case ev, ok := <-live:
			if !ok {
				// The bus closed its side (e.g. a Redis connection error
				// tearing down the subscription) — not a normal-close
				// signal for the client. Nothing more will ever arrive on
				// this channel, so there's nothing left to wait on.
				return
			}
			if ev.Seq != 0 {
				if ev.Seq <= *watermark {
					continue // already delivered during replay — see Stream's doc comment
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
	return writeWithTimeout(ctx, conn, encoded)
}

// writeReady sends the {"type":"ready","last_seq":N} frame marking the end
// of replay — see streamReadyMessage's doc comment.
func writeReady(ctx context.Context, conn *websocket.Conn, lastSeq int64) bool {
	encoded, err := json.Marshal(streamReadyMessage{Type: "ready", LastSeq: lastSeq})
	if err != nil {
		slog.Error("stream: failed to encode ready frame", "error", err)
		return true
	}
	return writeWithTimeout(ctx, conn, encoded)
}

// writeWithTimeout bounds a single write with ioTimeout (see its doc
// comment) rather than letting conn.Write block on ctx alone — coder/
// websocket's Write has no timeout of its own, so a peer that stops
// draining its TCP receive buffer (not the same as disconnecting, which
// ctx.Done() already covers) would otherwise stall this goroutine
// indefinitely.
func writeWithTimeout(ctx context.Context, conn *websocket.Conn, encoded []byte) bool {
	writeCtx, cancel := context.WithTimeout(ctx, ioTimeout)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, encoded) == nil
}

func closeStream(conn *websocket.Conn, code websocket.StatusCode, reason string) {
	_ = conn.Close(code, reason)
}

package handlers

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"ai-chat/internal/auth"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/modules/themebuild"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func getenvOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func openStreamTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&charset=utf8mb4&loc=UTC&clientFoundRows=true",
		getenvOr("DB_USERNAME", "root"), os.Getenv("DB_PASSWORD"),
		getenvOr("DB_HOST", "127.0.0.1"), getenvOr("DB_PORT", "3306"), getenvOr("DB_DATABASE", "ai_chat"))
	conn, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Skipf("skipping: could not open test database: %v", err)
	}
	if err := conn.Ping(); err != nil {
		_ = conn.Close()
		t.Skipf("skipping: test database not reachable: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// fakeAuthMiddleware stands in for auth.Middleware: it sets the same
// context key auth.Middleware sets (auth.Identity, under Gin key
// "auth_identity" — see internal/auth/middleware.go's ctxIdentityKey),
// without a real FlowPOS server to introspect a token against. Every
// request in this test is "authenticated" as tenantID. Used by handlers
// still behind auth.Middleware (see preview_test.go) — the Stream handler
// itself no longer is, see fakeFlowposServer below.
func fakeAuthMiddleware(tenantID uint64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("auth_identity", auth.Identity{TenantID: tenantID, UserID: 1})
		c.Next()
	}
}

// fakeFlowposServer stands in for the real FlowPOS /user endpoint that
// auth.WebSocketAuth introspects against — the Stream handler now
// authenticates itself (see auth.WebSocketAuth), unlike the rest of this
// package's handlers, which sit behind auth.Middleware and can be tested
// against a fake gin.HandlerFunc instead. Every token this server sees
// resolves to one active user belonging to exactly one tenant, tenantID.
func fakeFlowposServer(t *testing.T, tenantID uint64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"status": true,
			"data": {
				"user": {
					"id": 1, "name": "Test User", "email": "test@example.com", "is_active": true,
					"tenants": [{"id": %d, "slug": "test", "business_name": "Test", "role": {"id": 1, "name": "owner", "permissions": []}}]
				},
				"defaultTenant": {"id": %d}
			}
		}`, tenantID, tenantID)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// authSubprotocols builds the Sec-WebSocket-Protocol entries a real browser
// client sends (see auth.WebSocketAuth / auth.parseWebSocketSubprotocols) —
// the token is base64url-encoded since it's carried as a subprotocol, not a
// header.
func authSubprotocols(token string, tenantID uint64) []string {
	return []string{
		"bearer." + base64.RawURLEncoding.EncodeToString([]byte(token)),
		"tenant." + strconv.FormatUint(tenantID, 10),
	}
}

// readReady reads one frame and asserts it's the {"type":"ready"} frame
// marking the end of replay (see streamReadyMessage).
func readReady(t *testing.T, conn *websocket.Conn, wantLastSeq int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("expected a text message, got %v", typ)
	}
	var got struct {
		Type    string `json:"type"`
		LastSeq int64  `json:"last_seq"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("failed to decode ready frame: %v (%s)", err, data)
	}
	if got.Type != "ready" || got.LastSeq != wantLastSeq {
		t.Fatalf("expected ready frame with last_seq=%d, got %+v", wantLastSeq, got)
	}
}

func TestStreamHandler_EchoesOfferedSubprotocolOnAccept(t *testing.T) {
	conn := openStreamTestDB(t)
	chatRepo := chat.NewRepository(conn)
	chatSvc := chat.NewService(chatRepo)
	buildRepo := themebuild.NewRepository(conn)
	buildSvc := themebuild.NewService(buildRepo, chatSvc, nil, nil, nil)

	tenantID := uint64(time.Now().UnixNano())
	ctx := context.Background()
	ch, err := chatSvc.GetOrCreateChat(ctx, tenantID, themebuild.ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}

	flowpos := fakeFlowposServer(t, tenantID)
	authClient := auth.NewClient(flowpos.URL, 5*time.Second)
	authCache := auth.NewMemoryCache()
	t.Cleanup(authCache.Close)

	router := gin.New()
	router.GET("/chats/:chatId/stream", NewStreamHandler(chatSvc, buildSvc, authClient, authCache, time.Minute, time.Minute, nil).Stream)
	ts := httptest.NewServer(router)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/chats/" + ch.ID + "/stream"
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()

	offered := authSubprotocols("test-token", tenantID)
	wsConn, resp, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{ //nolint:bodyclose // Dial closes resp.Body internally; see its doc comment.
		Subprotocols: offered,
	})
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	defer func() { _ = wsConn.CloseNow() }()

	// The 101 response must echo back exactly the "bearer.<...>" subprotocol
	// the client offered — a real browser's WebSocket constructor treats a
	// missing/mismatched Sec-WebSocket-Protocol response header as a failed
	// handshake, even though coder/websocket's own client (used everywhere
	// else in this file) doesn't check for it. See stream.go's Accept call.
	got := resp.Header.Get("Sec-WebSocket-Protocol")
	if got != offered[0] {
		t.Errorf("expected Sec-WebSocket-Protocol %q echoed back, got %q", offered[0], got)
	}
}

func TestStreamHandler_ReplaysThenDeliversLiveThenStaysOpenPastDone(t *testing.T) {
	conn := openStreamTestDB(t)
	rdb, err := themebuild.NewRedisClient(getenvOr("REDIS_URL", "redis://127.0.0.1:6379"))
	if err != nil {
		t.Fatalf("invalid test REDIS_URL: %v", err)
	}
	if pingErr := rdb.Ping(context.Background()).Err(); pingErr != nil {
		t.Skipf("skipping: test redis not reachable: %v", pingErr)
	}
	t.Cleanup(func() { _ = rdb.Close() })

	chatRepo := chat.NewRepository(conn)
	chatSvc := chat.NewService(chatRepo)
	buildRepo := themebuild.NewRepository(conn)
	buildSvc := themebuild.NewService(buildRepo, chatSvc, nil, nil, rdb)

	// A fresh tenant ID per run: chats are keyed on (tenant_id, type), so a
	// fixed constant would reuse the same chat (and its generation rows)
	// across repeated test runs, tripping the "one running generation per
	// chat" constraint on a chat a previous run left running.
	tenantID := uint64(time.Now().UnixNano())
	ctx := context.Background()
	ch, err := chatSvc.GetOrCreateChat(ctx, tenantID, themebuild.ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}

	genID := uuid.NewString()
	if err := buildRepo.StartGeneration(ctx, genID, ch.ID, tenantID); err != nil {
		t.Fatalf("StartGeneration failed: %v", err)
	}
	mustAppendEvent(t, ctx, buildRepo, genID, ch.ID, 1, themebuild.EventTypeStarted, struct{}{})
	mustAppendEvent(t, ctx, buildRepo, genID, ch.ID, 2, themebuild.EventTypeChecking, map[string]int{"attempt": 1})

	flowpos := fakeFlowposServer(t, tenantID)
	authClient := auth.NewClient(flowpos.URL, 5*time.Second)
	authCache := auth.NewMemoryCache()
	t.Cleanup(authCache.Close)

	router := gin.New()
	router.GET("/chats/:chatId/stream", NewStreamHandler(chatSvc, buildSvc, authClient, authCache, time.Minute, time.Minute, nil).Stream)
	ts := httptest.NewServer(router)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/chats/" + ch.ID + "/stream"
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	// Dial's own doc comment: "You never need to close resp.Body yourself" —
	// it's already closed internally by the time Dial returns.
	wsConn, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{ //nolint:bodyclose
		Subprotocols: authSubprotocols("test-token", tenantID),
	})
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	defer func() { _ = wsConn.CloseNow() }()

	// Replay: both pre-existing events, in order, followed by the "ready"
	// frame marking the end of replay (see streamReadyMessage).
	readEvent(t, wsConn, themebuild.EventTypeStarted, 1)
	readEvent(t, wsConn, themebuild.EventTypeChecking, 2)
	readReady(t, wsConn, 2)

	// Live delivery: publish a third event via Redis (as the real
	// eventEmitter would) and confirm it arrives without a second connect.
	mustAppendEvent(t, ctx, buildRepo, genID, ch.ID, 3, themebuild.EventTypeRepairing, map[string]int{"attempt": 1})
	publishRaw(t, rdb, ch.ID, themebuild.GenerationEvent{
		GenerationID: genID, ChatID: ch.ID, Seq: 3, Type: themebuild.EventTypeRepairing,
		Payload: json.RawMessage(`{"attempt":1}`), CreatedAt: time.Now().UTC(),
	})
	readEvent(t, wsConn, themebuild.EventTypeRepairing, 3)

	// A "done" event must NOT close the connection — the chat can still
	// receive another prompt, and the client shouldn't have to reconnect
	// for it (see Stream's doc comment / waitForLiveEvents).
	if err := buildRepo.EndGeneration(ctx, ch.ID, nil); err != nil {
		t.Fatalf("EndGeneration failed: %v", err)
	}
	mustAppendEvent(t, ctx, buildRepo, genID, ch.ID, 4, themebuild.EventTypeDone, map[string]string{"summary": "ok"})
	publishRaw(t, rdb, ch.ID, themebuild.GenerationEvent{
		GenerationID: genID, ChatID: ch.ID, Seq: 4, Type: themebuild.EventTypeDone,
		Payload: json.RawMessage(`{"summary":"ok"}`), CreatedAt: time.Now().UTC(),
	})
	readEvent(t, wsConn, themebuild.EventTypeDone, 4)

	// Prove the connection is genuinely still open and subscribed, not just
	// slow to close: a brand-new generation's event on the same chat must
	// still arrive on this same connection, no reconnect needed.
	genID2 := uuid.NewString()
	if err := buildRepo.StartGeneration(ctx, genID2, ch.ID, tenantID); err != nil {
		t.Fatalf("second StartGeneration failed: %v", err)
	}
	mustAppendEvent(t, ctx, buildRepo, genID2, ch.ID, 5, themebuild.EventTypeStarted, struct{}{})
	publishRaw(t, rdb, ch.ID, themebuild.GenerationEvent{
		GenerationID: genID2, ChatID: ch.ID, Seq: 5, Type: themebuild.EventTypeStarted,
		Payload: json.RawMessage(`{}`), CreatedAt: time.Now().UTC(),
	})
	readEvent(t, wsConn, themebuild.EventTypeStarted, 5)
}

func TestStreamHandler_UnknownChatRejectedBeforeUpgrade(t *testing.T) {
	conn := openStreamTestDB(t)
	chatRepo := chat.NewRepository(conn)
	chatSvc := chat.NewService(chatRepo)
	buildRepo := themebuild.NewRepository(conn)
	buildSvc := themebuild.NewService(buildRepo, chatSvc, nil, nil, nil)

	tenantID := uint64(999002)
	flowpos := fakeFlowposServer(t, tenantID)
	authClient := auth.NewClient(flowpos.URL, 5*time.Second)
	authCache := auth.NewMemoryCache()
	t.Cleanup(authCache.Close)

	router := gin.New()
	router.GET("/chats/:chatId/stream", NewStreamHandler(chatSvc, buildSvc, authClient, authCache, time.Minute, time.Minute, nil).Stream)
	ts := httptest.NewServer(router)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/chats/" + uuid.NewString() + "/stream"
	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// See the other Dial call in this file re: not needing to close resp.Body.
	_, resp, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{ //nolint:bodyclose
		Subprotocols: authSubprotocols("test-token", tenantID),
	})
	if err == nil {
		t.Error("expected the dial to fail for a chat that doesn't belong to this tenant")
	} else if resp != nil && resp.StatusCode != 404 {
		t.Errorf("expected a 404, got %d", resp.StatusCode)
	}
}

func mustAppendEvent(t *testing.T, ctx context.Context, repo *themebuild.Repository, genID, chatID string, seq int64, typ string, payload any) {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal event payload: %v", err)
	}
	err = repo.AppendGenerationEvent(ctx, themebuild.GenerationEvent{
		ID: uuid.NewString(), GenerationID: genID, ChatID: chatID, Seq: seq,
		Type: typ, Payload: encoded, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("AppendGenerationEvent failed: %v", err)
	}
}

// publishRaw publishes ev to the same Redis channel the real eventEmitter
// uses ("gen:{chat_id}" — see themebuild's unexported redisChannelForChat,
// mirrored here since it isn't exported outside that package).
func publishRaw(t *testing.T, rdb *redis.Client, chatID string, ev themebuild.GenerationEvent) {
	t.Helper()
	encoded, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("failed to marshal event: %v", err)
	}
	if err := rdb.Publish(context.Background(), "gen:"+chatID, encoded).Err(); err != nil {
		t.Fatalf("failed to publish event: %v", err)
	}
}

func readEvent(t *testing.T, conn *websocket.Conn, wantType string, wantSeq int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("expected a text message, got %v", typ)
	}
	var got struct {
		Seq  int64  `json:"seq"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("failed to decode event: %v (%s)", err, data)
	}
	if got.Type != wantType || got.Seq != wantSeq {
		t.Fatalf("expected type=%q seq=%d, got type=%q seq=%d", wantType, wantSeq, got.Type, got.Seq)
	}
}

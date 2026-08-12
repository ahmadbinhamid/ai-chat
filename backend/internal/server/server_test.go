package server

import (
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ai-chat/internal/config"

	_ "github.com/go-sql-driver/mysql"
)

// testConfig is the minimum Config New needs to construct without a real
// FlowPOS/Anthropic/database dependency being reachable — FakeAIMode skips
// the ANTHROPIC_API_KEY requirement (see ai.NewFake), and every other field
// here only needs to be well-formed, not point at something live: New
// itself makes no outbound call during construction (see server.go — conn
// is only touched inside the /health handler closure, at request time).
func testConfig() config.Config {
	return config.Config{
		Port:                         "8080",
		FlowposAPIBase:               "https://flowpos.example.test",
		AuthCacheTTL:                 time.Minute,
		AuthNegativeCacheTTL:         time.Minute,
		FlowposHTTPTimeout:           time.Second,
		AIProvider:                   "anthropic",
		FakeAIMode:                   true,
		FakeAIDelay:                  time.Millisecond,
		GenerationRateLimitPerMinute: 10,
	}
}

// lazyDB opens a *sql.DB against an address nothing listens on — sql.Open
// never dials (database/sql connections are lazy), so this is safe to pass
// anywhere a *sql.DB is required by construction but never queried by the
// test itself. The background reaper goroutine New starts does query it and
// will log connection errors — harmless noise, not a test failure, and
// exactly the "database temporarily unreachable" case that goroutine is
// already built to tolerate.
func lazyDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", "root:@tcp(127.0.0.1:1)/doesnotmatter")
	if err != nil {
		t.Fatalf("sql.Open failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newTestServer(t *testing.T, cfg config.Config) *Server {
	t.Helper()
	srv, err := New(cfg, lazyDB(t), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	t.Cleanup(srv.Close)
	return srv
}

// TestNew_MountsExpectedRoutes checks a representative sample of routes
// from every handler group New wires up — not every route (that would just
// duplicate server.go's route table in test form), enough to catch a
// wiring mistake (a handler never mounted, a method/path typo) without
// this test needing to change every time a new endpoint is added.
func TestNew_MountsExpectedRoutes(t *testing.T) {
	srv := newTestServer(t, testConfig())

	want := map[string]bool{
		http.MethodGet + " /health":                      false,
		http.MethodGet + " /api/v1/chat":                 false,
		http.MethodGet + " /api/v1/chat/status":          false,
		http.MethodPost + " /api/v1/chats/messages":      false,
		http.MethodPost + " /api/v1/chats/:chatId/apply": false,
		http.MethodGet + " /api/v1/chats/:chatId/draft":  false,
		http.MethodGet + " /api/v1/chats/:chatId/stream": false,
		http.MethodPost + " /api/v1/themes":              false,
	}

	for _, ri := range srv.engine.Routes() {
		key := ri.Method + " " + ri.Path
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}

	for route, found := range want {
		if !found {
			t.Errorf("expected route %q to be mounted, but it wasn't found in srv.engine.Routes()", route)
		}
	}
}

// TestNew_CORSFailsClosedWithNoOrigins covers the degradation pattern
// documented in server.go: an empty CORSAllowedOrigins must never fall back
// to a permissive "*" — it must block every cross-origin browser request,
// which shows up as no Access-Control-Allow-Origin header on the response
// regardless of what Origin the request claims.
func TestNew_CORSFailsClosedWithNoOrigins(t *testing.T) {
	cfg := testConfig()
	cfg.CORSAllowedOrigins = nil
	srv := newTestServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://dashboard.example.test")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no Access-Control-Allow-Origin header with CORS_ALLOWED_ORIGINS unset, got %q", got)
	}
}

// TestNew_CORSAllowsConfiguredOrigin is the positive-path counterpart to
// the fail-closed test above — without it, that test alone can't
// distinguish "CORS correctly blocked this origin" from "CORS middleware
// never runs at all."
func TestNew_CORSAllowsConfiguredOrigin(t *testing.T) {
	cfg := testConfig()
	cfg.CORSAllowedOrigins = []string{"https://dashboard.example.test"}
	srv := newTestServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://dashboard.example.test")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://dashboard.example.test" {
		t.Fatalf("expected Access-Control-Allow-Origin to echo the allow-listed origin, got %q", got)
	}
}

package server

import (
	"bytes"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-chat/internal/config"

	"github.com/gin-gonic/gin"
	_ "github.com/go-sql-driver/mysql"
)

// testConfig is the minimum Config New needs to construct without a real dependency being
// reachable — New makes no outbound call during construction.
func testConfig() config.Config {
	return config.Config{
		Port:                         "8080",
		FlowposAPIBase:               "https://flowpos.example.test",
		AuthCacheTTL:                 time.Minute,
		AuthNegativeCacheTTL:         time.Minute,
		FlowposHTTPTimeout:           time.Second,
		FakeAIMode:                   true,
		FakeAIDelay:                  time.Millisecond,
		GenerationRateLimitPerMinute: 10,
		MaxRequestBodyBytes:          10 * 1024 * 1024,
	}
}

// lazyDB opens a *sql.DB against an address nothing listens on; sql.Open never dials, so
// it's safe wherever a *sql.DB is required but never queried by the test itself.
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

// TestNew_MountsExpectedRoutes checks a representative sample of routes from every handler
// group, enough to catch a wiring mistake without changing every time a new route is added.
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
		// /api/v1/themes (create-from-base) was removed; the AI builder only edits an
		// existing theme, never creates one.
		http.MethodPost + " /api/v1/themes/:slug/preview": false,
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

// TestNew_CORSFailsClosedWithNoOrigins checks an empty CORSAllowedOrigins never falls back
// to a permissive "*" — it must block every cross-origin request.
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

// TestNew_CORSAllowsConfiguredOrigin is the positive-path counterpart to the fail-closed
// test above, ruling out "CORS middleware never runs at all."
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

// echoBodyLen is a minimal handler for testing maxBodySize in isolation, deliberately not
// routed through auth.Middleware, which would reject the request before the body is read.
func echoBodyLen(c *gin.Context) {
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, c.Request.Body); err != nil {
		c.String(http.StatusBadRequest, "%v", err)
		return
	}
	c.String(http.StatusOK, "%d bytes", buf.Len())
}

func TestMaxBodySize_RejectsBodyOverLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(maxBodySize(10))
	r.POST("/echo", echoBodyLen)

	req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(strings.Repeat("a", 1000)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected reading a body past the limit to fail with 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMaxBodySize_AllowsBodyUnderLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(maxBodySize(1000))
	r.POST("/echo", echoBodyLen)

	req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader("small body"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected a body under the limit to be read fine, got %d: %s", rec.Code, rec.Body.String())
	}
}

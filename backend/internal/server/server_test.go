package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-chat/internal/aicatalog"
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

type fakePinger struct{ err error }

func (f fakePinger) Ping() error { return f.err }

func TestHealth(t *testing.T) {
	const secret = "dial tcp 10.0.0.5:3306: connect: connection refused (user ai_chat)"
	tests := []struct {
		name      string
		pingErr   error
		wantCode  int
		wantBuild bool
	}{
		{"database reachable reports ok with build", nil, http.StatusOK, true},
		{"database unreachable reports only unhealthy", errors.New(secret), http.StatusServiceUnavailable, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			r := gin.New()
			r.GET("/health", healthHandler(fakePinger{tt.pingErr}, slog.New(slog.NewTextHandler(&logs, nil))))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.wantCode, rec.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode /health: %v (%s)", err, rec.Body.String())
			}
			if _, hasBuild := body["build"]; hasBuild != tt.wantBuild {
				t.Errorf("build present = %v, want %v (%s)", hasBuild, tt.wantBuild, rec.Body.String())
			}
			if tt.pingErr == nil {
				return
			}
			if rec.Body.String() != `{"status":"unhealthy"}` {
				t.Errorf("failure body = %s, want only {\"status\":\"unhealthy\"}", rec.Body.String())
			}
			if !strings.Contains(logs.String(), secret) {
				t.Errorf("expected the real ping error to be logged, got %q", logs.String())
			}
		})
	}
}

// The real route must not leak the driver's dial error either.
func TestHealth_RouteHidesDatabaseError(t *testing.T) {
	srv := newTestServer(t, testConfig())
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != `{"status":"unhealthy"}` {
		t.Fatalf("got %d %s, want 503 {\"status\":\"unhealthy\"}", rec.Code, rec.Body.String())
	}
}

func TestLogCatalog_NeverLogsKeys(t *testing.T) {
	const keyEnv, key = "SECRET_PROVIDER_KEY_ENV", "sk-super-secret-value"
	catalog := &aicatalog.Catalog{
		Providers:    map[string]aicatalog.Provider{"openrouter": {BaseURL: "https://openrouter.ai/api", APIKeyEnv: keyEnv}},
		Models:       []aicatalog.Model{{ID: "flash"}, {ID: "vision"}},
		DefaultModel: "flash",
		VisionModel:  "vision",
	}
	t.Setenv(keyEnv, key)
	var logs bytes.Buffer
	logCatalog(slog.New(slog.NewTextHandler(&logs, nil)), "config/ai-models.json", catalog)

	out := logs.String()
	for _, want := range []string{"config/ai-models.json", "openrouter=https://openrouter.ai/api", "default_model=flash", "vision_model=vision", "model_count=2"} {
		if !strings.Contains(out, want) {
			t.Errorf("catalogue log missing %q: %s", want, out)
		}
	}
	for _, leak := range []string{key, keyEnv} {
		if strings.Contains(out, leak) {
			t.Errorf("catalogue log leaked %q: %s", leak, out)
		}
	}
}

func TestNew_ProductionModelCatalogue(t *testing.T) {
	tests := []struct {
		name    string
		fake    bool
		wantErr bool
	}{
		{"production without AI_MODELS_CONFIG refuses to start", false, true},
		{"fake mode is exempt", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig()
			cfg.AppEnv = config.AppEnvProduction
			cfg.FakeAIMode = tt.fake
			cfg.APIKey = "sk-test"
			srv, err := New(cfg, lazyDB(t), slog.New(slog.DiscardHandler))
			if srv != nil {
				t.Cleanup(srv.Close)
			}
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("New error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && !strings.Contains(err.Error(), "AI_MODELS_CONFIG is required") {
				t.Errorf("error should name AI_MODELS_CONFIG, got %v", err)
			}
		})
	}
}

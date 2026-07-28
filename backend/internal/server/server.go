// Package server wires the HTTP engine, routes, and dependencies together.
package server

import (
	"database/sql"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/auth"
	"ai-chat/internal/config"
	"ai-chat/internal/logging"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/modules/themebuild"
	"ai-chat/internal/ratelimit"
	"ai-chat/internal/server/handlers"
	"ai-chat/internal/themefs"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
)

type Server struct {
	cfg       config.Config
	engine    *gin.Engine
	authCache *auth.MemoryCache
}

// New builds the router and mounts every route. AI generation being
// unavailable (no ANTHROPIC_API_KEY) is a startup error, not something this
// service degrades around — generation is the entire product here, unlike
// a marketplace mini-app where AI is one optional feature among many.
func New(cfg config.Config, conn *sql.DB, logger *slog.Logger) (*Server, error) {
	useJSONFieldNames()

	generator, err := ai.New(cfg.AnthropicAPIKey, cfg.AnthropicModel, cfg.AnthropicEffort)
	if err != nil {
		return nil, err
	}
	store := themefs.NewStore(cfg.ThemeStorageRoot)

	chatRepo := chat.NewRepository(conn)
	chatSvc := chat.NewService(chatRepo)

	buildRepo := themebuild.NewRepository(conn)
	buildSvc := themebuild.NewService(buildRepo, chatSvc, generator, store)

	limiter := ratelimit.NewPerTenantLimiter(cfg.GenerationRateLimitPerMinute)

	chatHandler := handlers.NewChatHandler(chatSvc, buildSvc)
	messageHandler := handlers.NewMessageHandler(buildSvc, limiter)

	flowposClient := auth.NewClient(cfg.FlowposAPIBase, cfg.FlowposHTTPTimeout)
	authCache := auth.NewMemoryCache()

	r := gin.New()
	r.Use(gin.Recovery(), logging.Middleware(logger))

	// The tenant dashboard calls this API directly from the browser (it is
	// a native React page, not an iframed mini-app), so it needs real CORS
	// — not just a permissive "*", since that combined with a future
	// cookie-based auth mode would be a real hole. Origins are explicitly
	// allow-listed rather than wildcarded; no origins configured => no
	// cross-origin browser calls succeed (fails closed, not open).
	if len(cfg.CORSAllowedOrigins) > 0 {
		r.Use(cors.New(cors.Config{
			AllowOrigins: cfg.CORSAllowedOrigins,
			AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete},
			// Authorization carries the FlowPOS bearer token internal/auth
			// forwards upstream; X-Tenant-Id is the optional tenant-switch
			// header (see internal/auth's Middleware).
			AllowHeaders:     []string{"Content-Type", "Authorization", "X-Tenant-Id"},
			ExposeHeaders:    []string{"X-Request-Id"},
			AllowCredentials: false,
			MaxAge:           12 * time.Hour,
		}))
	} else {
		logger.Warn("CORS_ALLOWED_ORIGINS is empty — no browser origin will be able to call this API cross-origin")
	}

	r.GET("/health", func(c *gin.Context) {
		if err := conn.Ping(); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "db": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	api := r.Group("/api/v1")

	// Every route here authenticates by delegating to FlowPOS — see
	// internal/auth's package doc comment. There is no local auth system.
	identified := api.Group("")
	identified.Use(auth.Middleware(flowposClient, authCache, cfg.AuthCacheTTL, cfg.AuthNegativeCacheTTL))

	identified.GET("/chat", chatHandler.Get)
	identified.POST("/chats/messages", messageHandler.Send)

	return &Server{cfg: cfg, engine: r, authCache: authCache}, nil
}

// Close releases resources the server started that outlive a single
// request — currently just the auth cache's background sweep goroutine.
// Called during graceful shutdown (see cmd/server/main.go).
func (s *Server) Close() {
	s.authCache.Close()
}

// Handler exposes the underlying http.Handler — used both by Run (wrapped
// in an *http.Server for graceful shutdown, see cmd/server/main.go) and by
// in-process HTTP tests via httptest.
func (s *Server) Handler() http.Handler {
	return s.engine
}

// Addr is the configured listen address.
func (s *Server) Addr() string {
	return ":" + s.cfg.Port
}

// useJSONFieldNames makes the binding validator report a field's JSON name
// (e.g. "prompt") instead of its Go struct name (e.g. "Prompt") in errors.
func useJSONFieldNames() {
	v, ok := binding.Validator.Engine().(*validator.Validate)
	if !ok {
		return
	}
	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
		if name == "-" {
			return ""
		}
		return name
	})
}

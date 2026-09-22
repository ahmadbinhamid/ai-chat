// Package server wires the HTTP engine, routes, and dependencies together.
package server

import (
	"context"
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
	cfg          config.Config
	engine       *gin.Engine
	authCache    *auth.MemoryCache
	reaperCancel context.CancelFunc
}

// New builds the router and mounts every route. AI generation being unavailable is a
// startup error, not something this service degrades around — generation is the whole product.
func New(cfg config.Config, conn *sql.DB, logger *slog.Logger) (*Server, error) {
	useJSONFieldNames()

	streamTimeouts := ai.StreamTimeouts{
		Idle:            cfg.StreamIdleTimeout,
		FirstTokenEdit:  cfg.FirstTokenTimeoutEdit,
		FirstTokenBrand: cfg.FirstTokenTimeoutBrand,
		FirstTokenCopy:  cfg.FirstTokenTimeoutCopy,
		FirstTokenPages: cfg.FirstTokenTimeoutPages,
	}

	var generator *ai.Generator
	if cfg.FakeAIMode {
		logger.Warn("AI_CHAT_FAKE_MODE is enabled — every generation returns a canned no-op result, " +
			"the AI provider is never called, and nothing is ever written to a theme. Do not leave this on.")
		generator = ai.NewFake(cfg.FakeAIDelay)
	} else {
		var err error
		generator, err = ai.New(cfg.APIKey, cfg.BaseURL, cfg.Model, cfg.Effort, cfg.VisionModel, cfg.MaxTokens, streamTimeouts)
		if err != nil {
			return nil, err
		}
	}
	store := themefs.NewStore(cfg.FlowposAPIBase)

	rdb, err := themebuild.NewRedisClient(cfg.RedisURL)
	if err != nil {
		return nil, err
	}
	if rdb == nil {
		logger.Warn("REDIS_URL is not set — generation events still persist to generation_events, " +
			"but won't publish live to a WebSocket connected to a different replica than the one running the generation")
	}

	chatRepo := chat.NewRepository(conn)
	chatSvc := chat.NewService(chatRepo)

	buildRepo := themebuild.NewRepository(conn)
	buildSvc := themebuild.NewService(buildRepo, chatSvc, generator, store, rdb)
	buildSvc.SetHistorySummarizationEnabled(cfg.HistorySummarizationEnabled)

	limiter := ratelimit.NewPerTenantLimiter(cfg.GenerationRateLimitPerMinute)

	flowposClient := auth.NewClient(cfg.FlowposAPIBase, cfg.FlowposHTTPTimeout)
	authCache := auth.NewMemoryCache()

	chatHandler := handlers.NewChatHandler(chatSvc, buildSvc)
	messageHandler := handlers.NewMessageHandler(buildSvc, limiter)
	streamHandler := handlers.NewStreamHandler(chatSvc, buildSvc, flowposClient, authCache, cfg.AuthCacheTTL, cfg.AuthNegativeCacheTTL, cfg.CORSAllowedOrigins)
	revertHandler := handlers.NewRevertHandler(buildSvc)
	previewHandler := handlers.NewPreviewHandler(buildSvc)
	previewHandler.SetProductsFetchTimeout(cfg.FlowposHTTPTimeout)
	queueHandler := handlers.NewQueueHandler(buildSvc)
	applyHandler := handlers.NewApplyHandler(buildSvc)
	draftHandler := handlers.NewDraftHandler(buildSvc)
	assetHandler := handlers.NewAssetHandler(buildSvc)
	attachmentHandler := handlers.NewAttachmentHandler(buildSvc)

	r := gin.New()
	r.Use(gin.Recovery(), logging.Middleware(logger), maxBodySize(cfg.MaxRequestBodyBytes))

	// The tenant dashboard calls this API directly from the browser, so it
	// needs real CORS — origins are explicitly allow-listed, never "*".
	if len(cfg.CORSAllowedOrigins) > 0 {
		r.Use(cors.New(cors.Config{
			AllowOrigins: cfg.CORSAllowedOrigins,
			// DELETE is needed for the queue-cancel routes' preflight.
			AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete},
			// X-Tenant-Id is the optional tenant-switch header (see auth.Middleware).
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
	identified.GET("/chat/status", chatHandler.Status)
	identified.POST("/chats/messages", messageHandler.Send)
	identified.POST("/chats/:chatId/messages/:messageId/revert", revertHandler.Revert)
	identified.GET("/chats/:chatId/messages/:messageId/attachments/:attachmentId", attachmentHandler.Get)
	identified.DELETE("/chats/:chatId/queue/:generationId", queueHandler.Cancel)
	identified.DELETE("/chats/:chatId/queue", queueHandler.CancelAll)
	identified.POST("/chats/:chatId/apply", applyHandler.Apply)
	identified.POST("/chats/:chatId/discard", applyHandler.Discard)
	identified.GET("/chats/:chatId/draft", draftHandler.Files)
	identified.POST("/chats/:chatId/draft/edit", draftHandler.SaveManualEdit)
	identified.GET("/preview/context", previewHandler.Context)
	identified.GET("/theme-assets/*path", assetHandler.Get)
	identified.POST("/themes/:slug/preview", previewHandler.Preview)

	// Not in `identified`: a browser WebSocket can't set an Authorization header, so this
	// route authenticates via Sec-WebSocket-Protocol subprotocols instead.
	api.GET("/chats/:chatId/stream", streamHandler.Stream)

	// Runs immediately and then every minute until Close cancels it — independent of any
	// single request's lifecycle, so it needs its own long-lived context.
	reaperCtx, reaperCancel := context.WithCancel(context.Background())
	go buildSvc.RunReaper(reaperCtx)

	return &Server{cfg: cfg, engine: r, authCache: authCache, reaperCancel: reaperCancel}, nil
}

// Close releases resources the server started that outlive a single request — the auth
// cache's sweep goroutine and the stale-generation reaper.
func (s *Server) Close() {
	s.authCache.Close()
	s.reaperCancel()
}

// Handler exposes the underlying http.Handler, used both by Run and by in-process HTTP tests.
func (s *Server) Handler() http.Handler {
	return s.engine
}

// Addr is the configured listen address.
func (s *Server) Addr() string {
	return ":" + s.cfg.Port
}

// maxBodySize caps every request body at limit bytes; a read past it fails with
// *http.MaxBytesError, surfaced as an ordinary 400 by each handler's bind path.
func maxBodySize(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		}
		c.Next()
	}
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

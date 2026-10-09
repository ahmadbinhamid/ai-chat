// Package server wires the HTTP engine, routes, and dependencies together.
package server

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/aicatalog"
	"ai-chat/internal/auth"
	"ai-chat/internal/buildinfo"
	"ai-chat/internal/config"
	"ai-chat/internal/logging"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/modules/themebuild"
	"ai-chat/internal/ratelimit"
	"ai-chat/internal/safego"
	"ai-chat/internal/server/handlers"
	"ai-chat/internal/themefs"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
)

// modelListTimeout bounds the startup model check, so a slow provider only delays startup, never blocks it.
const modelListTimeout = 15 * time.Second

type Server struct {
	cfg          config.Config
	engine       *gin.Engine
	authCache    *auth.MemoryCache
	reaperCancel context.CancelFunc
	builder      *themebuild.Service
}

// DrainGenerations waits for running generations, up to SHUTDOWN_DRAIN_SECONDS, while HTTP keeps serving.
func (s *Server) DrainGenerations(ctx context.Context) (finished, stillRunning int) {
	return s.builder.Drain(ctx, s.cfg.ShutdownDrain)
}

// RequeueUnfinished re-queues generations still running at the drain limit, to resume after the restart.
func (s *Server) RequeueUnfinished(ctx context.Context) (requeued, failed int) {
	return s.builder.RequeueUnfinished(ctx)
}

// New builds the router and mounts every route. AI generation being unavailable is a
// startup error, not something this service degrades around — generation is the whole product.
func New(cfg config.Config, conn *sql.DB, logger *slog.Logger) (*Server, error) {
	useJSONFieldNames()

	if err := cfg.ValidateLockBackend(); err != nil {
		return nil, err
	}

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
		catalog, err := cfg.ModelCatalog()
		if err != nil {
			return nil, err
		}
		logCatalog(logger, cfg.ModelsConfig, catalog)
		generator, err = ai.New(catalog, os.LookupEnv, cfg.MaxTokens, streamTimeouts)
		if err != nil {
			return nil, err
		}
		verifyCtx, cancel := context.WithTimeout(context.Background(), modelListTimeout)
		err = generator.VerifyModels(verifyCtx)
		cancel()
		if err != nil {
			return nil, err
		}
	}
	store := themefs.NewStore(cfg.FlowposAPIBase)

	rdb, err := themebuild.NewRedisClient(cfg.RedisURL)
	if err != nil {
		return nil, err
	}
	if rdb != nil {
		logger.Info("theme lock backend", "backend", "redis")
	} else {
		logger.Info("theme lock backend", "backend", "in-process", "single_replica", cfg.SingleReplica)
		logger.Warn("REDIS_URL is not set — generation events still persist to generation_events, " +
			"but won't publish live to a WebSocket connected to a different replica than the one running the generation")
	}

	chatRepo := chat.NewRepository(conn)
	chatSvc := chat.NewService(chatRepo)

	buildRepo := themebuild.NewRepository(conn)
	buildSvc := themebuild.NewService(buildRepo, chatSvc, generator, store, rdb)
	buildSvc.SetHistorySummarizationEnabled(cfg.HistorySummarizationEnabled)
	buildSvc.SetPlacedImageMaxBytes(cfg.PlacedImageMaxBytes)
	buildSvc.SetModelCatalog(generator.Catalog())
	buildSvc.SetGenerationLimits(cfg.MaxConcurrentGenerations, cfg.MaxConcurrentGenerationsPerTenant)
	buildSvc.SetLargeThemeLimits(ai.LargeThemeLimits{Pages: cfg.LargeThemePages, Files: cfg.LargeThemeFiles})
	// Limits are per replica: N replicas allow N times as many concurrent generations.
	logger.Info("generation concurrency limits (0 = unlimited)",
		"max_concurrent", cfg.MaxConcurrentGenerations, "max_concurrent_per_tenant", cfg.MaxConcurrentGenerationsPerTenant)

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
	modelsHandler := handlers.NewModelsHandler(buildSvc)
	clientTimingHandler := handlers.NewClientTimingHandler(buildSvc)

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

	r.GET("/health", healthHandler(conn, logger))

	api := r.Group("/api/v1")

	// Every route here authenticates by delegating to FlowPOS — see
	// internal/auth's package doc comment. There is no local auth system.
	identified := api.Group("")
	identified.Use(auth.Middleware(flowposClient, authCache, cfg.AuthCacheTTL, cfg.AuthNegativeCacheTTL))
	identified.Use(handlers.ResumeAwaitingGenerations(buildSvc))

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
	identified.POST("/chats/:chatId/generations/:generationId/client-timing", clientTimingHandler.Record)
	identified.GET("/preview/context", previewHandler.Context)
	identified.GET("/models", modelsHandler.List)
	identified.GET("/theme-assets/*path", assetHandler.Get)
	identified.POST("/themes/:slug/preview", previewHandler.Preview)

	// Not in `identified`: a browser WebSocket can't set an Authorization header, so this
	// route authenticates via Sec-WebSocket-Protocol subprotocols instead.
	api.GET("/chats/:chatId/stream", streamHandler.Stream)

	// Runs immediately and then every minute until Close cancels it — independent of any
	// single request's lifecycle, so it needs its own long-lived context.
	// Before the reaper's first sweep, which would otherwise fail queued turns whose tokens died with the old process.
	buildSvc.PrepareResume(context.Background())
	reaperCtx, reaperCancel := context.WithCancel(context.Background())
	go func() {
		defer safego.Recover("themebuild.RunReaper")
		buildSvc.RunReaper(reaperCtx)
	}()

	return &Server{cfg: cfg, engine: r, authCache: authCache, reaperCancel: reaperCancel, builder: buildSvc}, nil
}

type pinger interface {
	Ping() error
}

// healthHandler is public and unauthenticated, so a failure response carries no error text or build identity.
func healthHandler(db pinger, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := db.Ping(); err != nil {
			logger.Error("health check: database ping failed", "error", err)
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "build": buildinfo.Get()})
	}
}

// logCatalog records which catalogue is live; it must never log api keys or the api_key_env variable names.
func logCatalog(logger *slog.Logger, modelsConfig string, catalog *aicatalog.Catalog) {
	source := modelsConfig
	if source == "" {
		source = "env"
	}
	names := make([]string, 0, len(catalog.Providers))
	for name := range catalog.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	providers := make([]string, 0, len(names))
	for _, name := range names {
		providers = append(providers, name+"="+catalog.Providers[name].BaseURL)
	}
	logger.Info("ai model catalogue loaded",
		"source", source,
		"providers", providers,
		"default_model", catalog.DefaultModel,
		"vision_model", catalog.VisionModel,
		"model_count", len(catalog.Models))
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

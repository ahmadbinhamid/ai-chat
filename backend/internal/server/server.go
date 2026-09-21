// Package server wires the HTTP engine, routes, and dependencies together.
package server

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/auth"
	"ai-chat/internal/builderexamples"
	"ai-chat/internal/builderintelligence"
	"ai-chat/internal/buildershadow"
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

// New builds the router and mounts every route. AI generation being
// unavailable (no ANTHROPIC_API_KEY) is a startup error, not something this
// service degrades around — generation is the entire product here, unlike
// a marketplace mini-app where AI is one optional feature among many.
func New(cfg config.Config, conn *sql.DB, logger *slog.Logger) (*Server, error) {
	useJSONFieldNames()

	var generator *ai.Generator
	switch {
	case cfg.FakeAIMode:
		if !cfg.AllowFakeAIMode {
			return nil, fmt.Errorf("AI_CHAT_FAKE_MODE is set without AI_CHAT_ALLOW_FAKE_MODE=true — refusing canned generation in this environment")
		}
		logger.Warn("AI_CHAT_FAKE_MODE is enabled — every generation returns a canned no-op result, " +
			"the AI provider is never called, and nothing is ever written to a theme. Do not leave this on.")
		generator = ai.NewFake(cfg.FakeAIDelay)
	case cfg.AIProvider == "deepseek":
		var err error
		generator, err = ai.New(cfg.DeepSeekAPIKey, cfg.DeepSeekBaseURL, cfg.DeepSeekModel, cfg.Effort, cfg.DeepSeekVisionModel, cfg.MaxTokens)
		if err != nil {
			return nil, err
		}
	default:
		var err error
		generator, err = ai.New(cfg.AnthropicAPIKey, "", cfg.AnthropicModel, cfg.Effort, cfg.AnthropicVisionModel, cfg.MaxTokens)
		if err != nil {
			return nil, err
		}
	}
	if !cfg.FakeAIMode {
		generator.SetTokenBudgets(ai.TokenBudgets{
			Interactive: cfg.MaxTokensInteractive,
			Brand:       cfg.MaxTokensBrand,
			Complex:     cfg.MaxTokensComplex,
			Repair:      cfg.MaxTokensRepair,
		})
		generator.SetStreamIdleTimeout(cfg.StreamIdleTimeout)
		generator.SetStreamFirstTokenTimeout(cfg.StreamFirstTokenTimeout)
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
	buildSvc.SetBuilderPlanEnabled(cfg.BuilderPlanEnabled)
	if cfg.BuilderPlanEnabled {
		logger.Info("builderplan observation enabled")
	}
	biSvc := builderintelligence.New(builderintelligence.Config{
		Enabled:  cfg.BuilderLocalLMEnabled,
		Provider: cfg.BuilderLocalLMProvider,
		URL:      cfg.BuilderLocalLMURL,
		Model:    cfg.BuilderLocalLMModel,
		Timeout:  time.Duration(cfg.BuilderLocalLMTimeoutMs) * time.Millisecond,
	})
	buildSvc.SetBuilderIntelligence(biSvc)
	if cfg.BuilderLocalLMEnabled {
		logger.Info("builder intelligence enabled",
			"provider", biSvc.ProviderName(),
			"timeout_ms", cfg.BuilderLocalLMTimeoutMs,
			"url_set", cfg.BuilderLocalLMURL != "",
		)
	}
	shadow := buildershadow.New(buildershadow.Config{
		Enabled:    cfg.BuilderShadowLMEnabled,
		Provider:   cfg.BuilderShadowLMProvider,
		URL:        cfg.BuilderShadowLMURL,
		Model:      cfg.BuilderShadowLMModel,
		Timeout:    time.Duration(cfg.BuilderShadowLMTimeoutMs) * time.Millisecond,
		SampleRate: cfg.BuilderShadowSampleRate,
		Blocking:   false, // never block production generation
	})
	buildSvc.SetBuilderShadow(shadow)
	if cfg.BuilderShadowLMEnabled {
		logger.Info("builder shadow candidate enabled",
			"provider", shadow.CandidateName(),
			"timeout_ms", cfg.BuilderShadowLMTimeoutMs,
			"url_set", cfg.BuilderShadowLMURL != "",
			"sample_rate", cfg.BuilderShadowSampleRate,
			"affects_production", false,
		)
	}
	if cfg.BuilderTrainingDataEnabled {
		exStore := builderexamples.NewSQLStore(conn)
		exCollector := builderexamples.NewCollector(builderexamples.Config{
			Enabled:              true,
			SampleRate:           cfg.BuilderTrainingSampleRate,
			RetentionDays:        cfg.BuilderTrainingRetentionDays,
			MaxRecords:           cfg.BuilderTrainingMaxRecords,
			StoreSanitizedPrompt: cfg.BuilderTrainingStoreSanitizedPrompt,
		}, exStore)
		buildSvc.SetBuilderExampleCollector(exCollector)
		logger.Info("builder training data collection enabled",
			"sample_rate", cfg.BuilderTrainingSampleRate,
			"retention_days", cfg.BuilderTrainingRetentionDays,
			"max_records", cfg.BuilderTrainingMaxRecords,
			"store_sanitized_prompt", cfg.BuilderTrainingStoreSanitizedPrompt,
		)
	}
	if cfg.ThemeWorkspaceDir != "" {
		buildSvc.SetThemeWorkspaceRoot(cfg.ThemeWorkspaceDir)
		logger.Info("theme workspace enabled", "dir", cfg.ThemeWorkspaceDir)
	}

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

	// The tenant dashboard calls this API directly from the browser (it is
	// a native React page, not an iframed mini-app), so it needs real CORS
	// — not just a permissive "*", since that combined with a future
	// cookie-based auth mode would be a real hole. Origins are explicitly
	// allow-listed rather than wildcarded; no origins configured => no
	// cross-origin browser calls succeed (fails closed, not open).
	if len(cfg.CORSAllowedOrigins) > 0 {
		r.Use(cors.New(cors.Config{
			AllowOrigins: cfg.CORSAllowedOrigins,
			// DELETE is needed for DELETE /chats/:chatId/queue/:generationId
			// (cancelling a queued prompt) — without it here, the browser's
			// CORS preflight for that route fails before the request itself
			// is even sent.
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

	// Not in the `identified` group: a browser WebSocket can't set an
	// Authorization header, so this route authenticates itself via
	// auth.WebSocketAuth (Sec-WebSocket-Protocol subprotocols) instead — see
	// StreamHandler's doc comment. GET /chat (above) stays as the polling
	// fallback for a client whose WebSocket can't connect (corporate proxy,
	// etc.) — this doesn't replace it.
	api.GET("/chats/:chatId/stream", streamHandler.Stream)

	// Runs immediately and then every minute until Close cancels it (see
	// themebuild.Service.RunReaper) — independent of any single request's
	// lifecycle, so it needs its own long-lived context.
	reaperCtx, reaperCancel := context.WithCancel(context.Background())
	go buildSvc.RunReaper(reaperCtx)

	return &Server{cfg: cfg, engine: r, authCache: authCache, reaperCancel: reaperCancel}, nil
}

// Close releases resources the server started that outlive a single
// request — the auth cache's background sweep goroutine and the stale-
// generation reaper. Called during graceful shutdown (see cmd/server/main.go).
func (s *Server) Close() {
	s.authCache.Close()
	s.reaperCancel()
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

// maxBodySize caps every request body this API accepts at limit bytes — see
// config.Config.MaxRequestBodyBytes's doc comment for why. http.MaxBytesReader
// makes any read past limit fail with a *http.MaxBytesError instead of
// silently allowing unbounded in-memory allocation; every handler already
// surfaces a body-read/decode failure as a normal 400 through its existing
// c.ShouldBindJSON + respondBindErr path (see handlers/errors.go), so a
// request that hits this limit gets a clear error, not a crash or a hang.
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

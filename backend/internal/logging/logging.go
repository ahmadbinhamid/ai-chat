// Package logging configures the process-wide structured logger (slog,
// JSON output) and a Gin middleware that logs one line per request with a
// correlation ID — replacing gin's plain-text default logger, which has no
// request ID and isn't machine-parseable. A request ID matters more here
// than in a typical CRUD service: tracing one chat "turn" end-to-end (tenant,
// chat, model, tokens, latency) is the main thing you'll want out of logs.
package logging

import (
	"log/slog"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// requestIDKey is the Gin context key the request ID is stored under.
const requestIDKey = "request_id"

// New builds the process-wide JSON logger, writing to stdout so it composes
// with whatever collects container/process output in every environment
// (local, CI, prod) without extra configuration.
func New() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// Middleware assigns a request ID (reusing an inbound X-Request-Id if the
// caller already set one, e.g. from an upstream gateway) and logs one
// structured line per request after it completes.
func Middleware(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-Id")
		if id == "" {
			id = uuid.NewString()
		}
		c.Set(requestIDKey, id)
		c.Header("X-Request-Id", id)

		start := time.Now()
		c.Next()

		logger.Info("request",
			"request_id", id,
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
			"tenant_id", c.GetUint64("tenant_id"),
		)
	}
}

// RequestID reads the current request's correlation ID, set by Middleware.
// Handlers use this to tag AI-call logs (model, tokens, latency) with the
// same ID as the surrounding HTTP request log line.
func RequestID(c *gin.Context) string {
	if v, ok := c.Get(requestIDKey); ok {
		if id, ok := v.(string); ok {
			return id
		}
	}
	return ""
}

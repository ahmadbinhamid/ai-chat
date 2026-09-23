// Package logging configures the process-wide structured JSON logger and a Gin middleware
// that logs one line per request with a correlation ID, for tracing a chat turn end-to-end.
package logging

import (
	"log/slog"
	"os"
	"time"

	"ai-chat/internal/auth"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// requestIDKey is the Gin context key the request ID is stored under.
const requestIDKey = "request_id"

// New builds the process-wide JSON logger, writing to stdout.
func New() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// Middleware assigns a request ID (reusing an inbound X-Request-Id if set) and logs one
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

		// 0 for routes with no auth (health check) or a request that never got past authentication.
		logger.Info("request",
			"request_id", id,
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
			"tenant_id", auth.TenantID(c),
		)
	}
}

// RequestID reads the current request's correlation ID, set by Middleware.
func RequestID(c *gin.Context) string {
	if v, ok := c.Get(requestIDKey); ok {
		if id, ok := v.(string); ok {
			return id
		}
	}
	return ""
}

// Package httpresponse centralizes JSON response shaping so handlers never
// hand-build gin.H{} envelopes ad hoc — every success response has the same
// {"data": ...} shape, every error the same {"error": ..., "code": ...} shape.
package httpresponse

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// OK writes a 200 with the given payload wrapped in {"data": ...}.
func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"data": data})
}

// Created writes a 201 with the given payload wrapped in {"data": ...}.
func Created(c *gin.Context, data any) {
	c.JSON(http.StatusCreated, gin.H{"data": data})
}

// Accepted writes a 202 with the given payload wrapped in {"data": ...} —
// for an endpoint that has queued work rather than finished it (see
// themebuild.Service.Generate).
func Accepted(c *gin.Context, data any) {
	c.JSON(http.StatusAccepted, gin.H{"data": data})
}

// NoContent writes a bare 204.
func NoContent(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

// Error writes {"error": message} (optionally with a stable "code" a client
// can branch on) at the given status. Handlers should reach for the
// sentinel-error mapping in server/handlers/errors.go rather than calling
// this directly wherever possible — this is the low-level primitive it's
// built on.
func Error(c *gin.Context, status int, message string, code string) {
	body := gin.H{"error": message}
	if code != "" {
		body["code"] = code
	}
	c.JSON(status, body)
}

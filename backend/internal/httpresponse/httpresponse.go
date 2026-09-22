// Package httpresponse centralizes JSON response shaping: every success response is
// {"data": ...}, every error {"error": ..., "code": ...}.
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

// Accepted writes a 202 with the payload wrapped in {"data": ...}, for queued not finished work.
func Accepted(c *gin.Context, data any) {
	c.JSON(http.StatusAccepted, gin.H{"data": data})
}

// NoContent writes a bare 204.
func NoContent(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

// Error writes {"error": message} (optionally with a "code" a client can branch on).
// Prefer server/handlers/errors.go's sentinel-error mapping over calling this directly.
func Error(c *gin.Context, status int, message string, code string) {
	body := gin.H{"error": message}
	if code != "" {
		body["code"] = code
	}
	c.JSON(status, body)
}

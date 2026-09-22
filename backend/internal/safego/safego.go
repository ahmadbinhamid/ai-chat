// Package safego guards against a panic in a background goroutine crashing the whole process —
// gin.Recovery() only covers gin's own per-request goroutine, not a `go func(){}()` it spawns.
package safego

import (
	"log/slog"
	"runtime/debug"
)

// Recover, deferred at the top of a goroutine, logs a panic instead of crashing the process.
// For a long-running loop, defer it per-iteration instead so one bad iteration doesn't end the loop.
func Recover(label string) {
	if r := recover(); r != nil {
		slog.Error("recovered panic in background goroutine",
			"goroutine", label,
			"panic", r,
			"stack", string(debug.Stack()),
		)
	}
}

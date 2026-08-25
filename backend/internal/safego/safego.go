// Package safego guards against a panic in a background goroutine taking
// down the whole process. gin.Recovery() (see server.go) only catches a
// panic in the goroutine gin itself dispatches per HTTP request — by Go's
// own recover() semantics, it does NOT protect a `go func(){}()` spawned
// from inside that handler (themebuild's generation runner, its heartbeat
// ticker, the Redis event-bus relay, auth's cache sweep loop). Any of those
// panicking today crashes ai-chat entirely, for every tenant's in-flight
// work, not just whatever triggered it.
package safego

import (
	"log/slog"
	"runtime/debug"
)

// Recover, deferred at the top of a goroutine (or a single loop iteration
// wrapped in its own closure — see the package doc comment), turns a panic
// into a logged error instead of letting it propagate and crash the
// process. label identifies which goroutine recovered, so a hit in the
// logs is traceable back to its source.
//
// Deferred at the top of an entire goroutine, a recovered panic ends that
// goroutine's work for good — the right call for a one-shot unit of work
// (e.g. running a single generation), where a further recovery mechanism
// already exists independently (themebuild's reaper picks up a generation
// that never finished). For a goroutine meant to keep running indefinitely
// (a ticker loop, a pubsub relay), defer this inside each iteration's own
// closure instead, so one bad iteration doesn't end the whole loop.
func Recover(label string) {
	if r := recover(); r != nil {
		slog.Error("recovered panic in background goroutine",
			"goroutine", label,
			"panic", r,
			"stack", string(debug.Stack()),
		)
	}
}

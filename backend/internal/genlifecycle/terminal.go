// Package genlifecycle holds pure, race-safe generation stream lifecycle
// helpers used by themebuild's eventEmitter and the WebSocket stream path.
// No DB, network, or disk I/O.
package genlifecycle

import (
	"sync/atomic"
)

// Terminal event types on the generation stream wire protocol.
const (
	EventDone      = "done"
	EventFailed    = "failed"
	EventCancelled = "cancelled"
)

// IsTerminal reports whether typ is one of the three mutually exclusive
// generation end states.
func IsTerminal(typ string) bool {
	switch typ {
	case EventDone, EventFailed, EventCancelled:
		return true
	default:
		return false
	}
}

// TerminalGuard ensures at most one terminal event is accepted for a single
// generation's emitter. First claim wins; later terminals and late
// non-terminal progress are rejected. Safe for concurrent TryClaim /
// RejectNonTerminal checks.
//
// Bound: one guard per eventEmitter (one generation). Never keyed in a
// global map by generation/chat ID.
type TerminalGuard struct {
	claimed atomic.Bool
	kind    atomic.Value // string; set only while claimed
}

// TryClaimTerminal records typ as this generation's sole terminal state.
// Returns true if this call won; false if a terminal was already claimed
// (caller must suppress the duplicate emit).
func (g *TerminalGuard) TryClaimTerminal(typ string) bool {
	if !IsTerminal(typ) {
		return false
	}
	if g.claimed.CompareAndSwap(false, true) {
		g.kind.Store(typ)
		return true
	}
	return false
}

// Claimed reports whether a terminal has already been accepted.
func (g *TerminalGuard) Claimed() bool {
	return g.claimed.Load()
}

// Kind returns the claimed terminal type, or "" if none.
func (g *TerminalGuard) Kind() string {
	v := g.kind.Load()
	if v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

// ShouldEmitNonTerminal is false once a terminal has been claimed — late
// progress/tool/repair events must not resurrect a finished generation.
func (g *TerminalGuard) ShouldEmitNonTerminal() bool {
	return !g.claimed.Load()
}

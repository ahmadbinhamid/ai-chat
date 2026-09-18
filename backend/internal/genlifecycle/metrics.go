package genlifecycle

import "sync/atomic"

// Process-scoped Phase 6 reliability counters. Never keyed by
// generation/chat/request ID — only totals for the process lifetime.
var (
	ConnectionAttempts         atomic.Int64
	DisconnectCount            atomic.Int64
	ReconnectCount             atomic.Int64 // reserved for client-side / future; backend increments on Stream re-entry only when useful
	DuplicateEventsSuppressed  atomic.Int64
	StaleEventsIgnored         atomic.Int64
	TerminalEventsEmitted      atomic.Int64
	TerminalEventsSuppressed   atomic.Int64
	EventSendFailures          atomic.Int64
	EventSendBlockedMs         atomic.Int64
	TerminalBusForcedDeliveries atomic.Int64
)

// Snapshot is a point-in-time copy of the process counters (for tests/logs).
type Snapshot struct {
	ConnectionAttempts          int64
	DisconnectCount             int64
	DuplicateEventsSuppressed   int64
	StaleEventsIgnored          int64
	TerminalEventsEmitted       int64
	TerminalEventsSuppressed    int64
	EventSendFailures           int64
	EventSendBlockedMs          int64
	TerminalBusForcedDeliveries int64
}

// ReadSnapshot returns current counter values.
func ReadSnapshot() Snapshot {
	return Snapshot{
		ConnectionAttempts:          ConnectionAttempts.Load(),
		DisconnectCount:             DisconnectCount.Load(),
		DuplicateEventsSuppressed:   DuplicateEventsSuppressed.Load(),
		StaleEventsIgnored:          StaleEventsIgnored.Load(),
		TerminalEventsEmitted:       TerminalEventsEmitted.Load(),
		TerminalEventsSuppressed:    TerminalEventsSuppressed.Load(),
		EventSendFailures:           EventSendFailures.Load(),
		EventSendBlockedMs:          EventSendBlockedMs.Load(),
		TerminalBusForcedDeliveries: TerminalBusForcedDeliveries.Load(),
	}
}

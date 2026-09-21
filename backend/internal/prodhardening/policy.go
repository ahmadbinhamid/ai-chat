// Package prodhardening documents and tests Phase 7 production load /
// failure bounds for the AI Theme Builder. Pure helpers only — no DB,
// network, or disk I/O in non-test code.
package prodhardening

import "time"

// Policy mirrors the compiled-in themebuild / ai / themefs / ratelimit /
// stream constants. Kept here so load tests and operators can see one
// source of truth without importing every package's unexported names.
// Values MUST stay in sync with the constants they document — see
// TestPolicyMatchesRuntime in the themebuild package.
type Policy struct {
	MaxQueueDepthPerChat           int
	MaxConcurrentRunningPerChat    int // always 1 (uniq_generations_running_chat)
	MaxThemeCheckRetries           int
	MaxInitialProposalGenerate     int
	MaxRepairGenerate              int
	MaxSimpleEditEscalations       int // at most one complex escalate
	MaxGenerateCallsWithEscalation int
	MaxToolIterationsPerGenerate   int
	StreamAccumulateMaxAttempts    int
	MaxGrepFilesScanned            int
	MaxGrepMatches                 int
	GrepMaxConcurrency             int
	ThemeCacheMaxEntries           int
	ThemeCacheMaxBytes             int64
	SubscriberBufferSize           int
	MaxGenerationEventsPerChat     int
	GenerationTimeout              time.Duration
	StreamFirstTokenTimeout        time.Duration
	StreamIdleTimeout              time.Duration
	WebSocketIOTimeout             time.Duration
	HTTPWriteTimeout               time.Duration
	HTTPReadTimeout                time.Duration
	GenerationRateLimitPerMinute   int
	MaxRequestBodyBytes            int64
	MaxReconnectAttemptsFrontend   int
	RateLimiterMaxTenants          int
	PendingTokensSoftCap           int
}

// DefaultPolicy returns the production-hardening policy snapshot used by
// Phase 7 tests. Numbers match the current repository defaults.
func DefaultPolicy() Policy {
	return Policy{
		MaxQueueDepthPerChat:           10,
		MaxConcurrentRunningPerChat:    1,
		MaxThemeCheckRetries:           2,
		MaxInitialProposalGenerate:     3, // retries+1
		MaxRepairGenerate:              2,
		MaxSimpleEditEscalations:       1,
		MaxGenerateCallsWithEscalation: 8, // 3+3+2
		MaxToolIterationsPerGenerate:   20,
		StreamAccumulateMaxAttempts:    2,
		MaxGrepFilesScanned:            500,
		MaxGrepMatches:                 200,
		GrepMaxConcurrency:             8,
		ThemeCacheMaxEntries:           512,
		ThemeCacheMaxBytes:             512 * 40_000, // 20 MiB
		SubscriberBufferSize:           32,
		MaxGenerationEventsPerChat:     200,
		GenerationTimeout:              10 * time.Minute,
		StreamFirstTokenTimeout:        45 * time.Second,
		StreamIdleTimeout:              12 * time.Second,
		WebSocketIOTimeout:             10 * time.Second,
		HTTPWriteTimeout:               30 * time.Second,
		HTTPReadTimeout:                30 * time.Second,
		GenerationRateLimitPerMinute:   10,
		MaxRequestBodyBytes:            45 * 1024 * 1024,
		MaxReconnectAttemptsFrontend:   5,
		RateLimiterMaxTenants:          4096,
		PendingTokensSoftCap:           10_000,
	}
}

// MaxProviderStreamAttemptsPerGeneration is the theoretical ceiling of
// provider stream accumulate attempts for one merchant generation under
// DefaultPolicy, including one simple-edit → complex escalation:
//
//	GenerateCalls × ToolIterations × StreamAttempts
//
// = 8 × 20 × 2 = 320
//
// This is a worst-case upper bound (every Generate runs the full tool loop
// and every model iteration exhausts stream retries). Real turns are far
// lower; the point is proving the product stays finite.
func MaxProviderStreamAttemptsPerGeneration(p Policy) int {
	return p.MaxGenerateCallsWithEscalation * p.MaxToolIterationsPerGenerate * p.StreamAccumulateMaxAttempts
}

// TimeoutHierarchyOK reports whether child timeouts sit under the parent
// generation budget (avoiding pointless work after the parent is dead).
func TimeoutHierarchyOK(p Policy) bool {
	if p.StreamFirstTokenTimeout >= p.GenerationTimeout {
		return false
	}
	if p.StreamIdleTimeout >= p.GenerationTimeout {
		return false
	}
	if p.WebSocketIOTimeout >= p.GenerationTimeout {
		return false
	}
	// HTTP write timeout only covers the 202 enqueue path, not generation.
	return p.HTTPWriteTimeout > 0 && p.HTTPReadTimeout > 0
}

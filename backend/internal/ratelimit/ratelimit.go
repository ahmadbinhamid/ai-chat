// Package ratelimit provides a per-tenant token-bucket limiter. It exists
// specifically for the generation endpoint: every other route in this
// service is a cheap DB read/write, but POST .../messages calls an LLM,
// which costs real money and multi-second latency per call — the one route
// in this codebase that actually needs throttling.
//
// This is an in-process limiter (a map of tenant ID -> *rate.Limiter behind
// a mutex). It only limits requests handled by the single instance it runs
// in — the same single-replica caveat the sibling apps' background sync
// scheduler already documents. Fine for one instance; if this service is
// ever horizontally scaled, replace with a shared store (Redis) instead of
// pretending the in-process version still works.
package ratelimit

import (
	"sync"

	"golang.org/x/time/rate"
)

// ratelimitMaxTenants bounds PerTenantLimiter's map size — one entry per
// tenant that has ever made a request, otherwise unbounded for the life of
// the process. Eviction (see limiterFor) is arbitrary, not LRU: cheap to
// implement, and a wrongly-evicted tenant only costs one fresh burst
// allowance on their next request, never a correctness problem — the same
// tradeoff historySummaryCache makes for the same reason (see
// themebuild/history_summary.go).
const ratelimitMaxTenants = 4096

// PerTenantLimiter hands out one token-bucket limiter per tenant, created on
// first use and reused after that.
type PerTenantLimiter struct {
	mu         sync.Mutex
	limiters   map[uint64]*rate.Limiter
	ratePerMin int
	burst      int
}

// NewPerTenantLimiter builds a limiter allowing ratePerMin requests/minute
// per tenant, with a burst equal to that same amount (so a tenant can spend
// their whole minute's allowance immediately rather than being forced to
// trickle one request at a time).
func NewPerTenantLimiter(ratePerMin int) *PerTenantLimiter {
	if ratePerMin < 1 {
		ratePerMin = 1
	}
	return &PerTenantLimiter{
		limiters:   make(map[uint64]*rate.Limiter),
		ratePerMin: ratePerMin,
		burst:      ratePerMin,
	}
}

// Allow reports whether tenantID may make one more request right now,
// consuming a token if so.
func (l *PerTenantLimiter) Allow(tenantID uint64) bool {
	return l.limiterFor(tenantID).Allow()
}

func (l *PerTenantLimiter) limiterFor(tenantID uint64) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	if lim, ok := l.limiters[tenantID]; ok {
		return lim
	}
	if len(l.limiters) >= ratelimitMaxTenants {
		// Evict one arbitrary entry — Go map iteration order is randomized,
		// so this is effectively a random eviction, not LRU. See
		// ratelimitMaxTenants' doc comment for why that's an acceptable
		// tradeoff here.
		for k := range l.limiters {
			delete(l.limiters, k)
			break
		}
	}
	// ratePerMin requests per minute == ratePerMin/60 requests per second.
	lim := rate.NewLimiter(rate.Limit(float64(l.ratePerMin)/60.0), l.burst)
	l.limiters[tenantID] = lim
	return lim
}

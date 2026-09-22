// Package ratelimit provides a per-tenant token-bucket limiter, mainly for the LLM-backed
package ratelimit

import (
	"sync"

	"golang.org/x/time/rate"
)

// ratelimitMaxTenants bounds the map size so it can't grow unbounded for the process
// lifetime. Eviction is arbitrary, not LRU — a wrongly-evicted tenant just gets a fresh burst.
const ratelimitMaxTenants = 4096

// PerTenantLimiter hands out one token-bucket limiter per tenant, created on
// first use and reused after that.
type PerTenantLimiter struct {
	mu         sync.Mutex
	limiters   map[uint64]*rate.Limiter
	ratePerMin int
	burst      int
}

// NewPerTenantLimiter builds a limiter allowing ratePerMin requests/minute per tenant,
// with burst equal to that amount so a tenant can spend it all at once.
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
		// Evict one arbitrary entry; Go map iteration order is randomized, so this is
		// effectively random eviction, not LRU.
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

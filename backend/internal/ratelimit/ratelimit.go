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
//
// Phase 7: the tenant→limiter map is bounded (maxTenants). When full, the
// least-recently-used entry is evicted so unique tenant IDs cannot grow
// process memory without bound.
package ratelimit

import (
	"container/list"
	"sync"

	"ai-chat/internal/prodhardening"

	"golang.org/x/time/rate"
)

// PerTenantLimiter hands out one token-bucket limiter per tenant, created on
// first use and reused after that.
type PerTenantLimiter struct {
	mu         sync.Mutex
	limiters   map[uint64]*limiterEntry
	lru        *list.List // front = most recently used
	ratePerMin int
	burst      int
	maxTenants int
}

type limiterEntry struct {
	tenantID uint64
	lim      *rate.Limiter
	elem     *list.Element
}

// NewPerTenantLimiter builds a limiter allowing ratePerMin requests/minute
// per tenant, with a burst equal to that same amount (so a tenant can spend
// their whole minute's allowance immediately rather than being forced to
// trickle one request at a time).
func NewPerTenantLimiter(ratePerMin int) *PerTenantLimiter {
	if ratePerMin < 1 {
		ratePerMin = 1
	}
	maxTenants := prodhardening.DefaultPolicy().RateLimiterMaxTenants
	if maxTenants < 1 {
		maxTenants = 4096
	}
	return &PerTenantLimiter{
		limiters:   make(map[uint64]*limiterEntry),
		lru:        list.New(),
		ratePerMin: ratePerMin,
		burst:      ratePerMin,
		maxTenants: maxTenants,
	}
}

// Allow reports whether tenantID may make one more request right now,
// consuming a token if so.
func (l *PerTenantLimiter) Allow(tenantID uint64) bool {
	return l.limiterFor(tenantID).Allow()
}

// Len returns how many tenant limiters are currently retained (tests /
// observability).
func (l *PerTenantLimiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.limiters)
}

func (l *PerTenantLimiter) limiterFor(tenantID uint64) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	if e, ok := l.limiters[tenantID]; ok {
		l.lru.MoveToFront(e.elem)
		return e.lim
	}
	for len(l.limiters) >= l.maxTenants {
		back := l.lru.Back()
		if back == nil {
			break
		}
		victim := back.Value.(uint64)
		l.lru.Remove(back)
		delete(l.limiters, victim)
		prodhardening.RateLimiterEvictions.Add(1)
	}
	lim := rate.NewLimiter(rate.Limit(float64(l.ratePerMin)/60.0), l.burst)
	elem := l.lru.PushFront(tenantID)
	l.limiters[tenantID] = &limiterEntry{tenantID: tenantID, lim: lim, elem: elem}
	return lim
}

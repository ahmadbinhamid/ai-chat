package auth

import (
	"context"
	"sync"
	"time"
)

// CacheEntry is a cached FlowPOS introspection result, keyed by hashed
// token (see cacheKey in middleware.go) — never a resolved Identity (see
// package doc comment for why). Negative marks a cached 401; every other
// field is zero-value when Negative is true.
type CacheEntry struct {
	UserID          uint64
	Name            string
	Email           string
	IsActive        bool
	Tenants         []Tenant
	DefaultTenantID *uint64
	Negative        bool
}

// Cache stores CacheEntry values behind hashed-token keys with independent
// positive/negative TTLs. Takes a context and returns an error specifically
// so a future Redis-backed implementation is a drop-in replacement here —
// MemoryCache always returns a nil error and ignores ctx, but Middleware
// still checks for one: a cache failure degrades to a live upstream call,
// never a 500.
type Cache interface {
	Get(ctx context.Context, key string) (entry CacheEntry, ok bool, err error)
	Set(ctx context.Context, key string, entry CacheEntry, ttl time.Duration) error
}

type cacheRow struct {
	entry     CacheEntry
	expiresAt time.Time
}

// MemoryCache is an in-process TTL cache — a map behind a mutex, the same
// shape as ratelimit.PerTenantLimiter. Only valid within a single instance;
// if this service is ever horizontally scaled, a miss on one replica after
// a hit on another just costs one extra upstream call, it doesn't break
// correctness — swap in a Redis-backed Cache instead of pretending this one
// still works across replicas.
//
// Unlike PerTenantLimiter's tenant-keyed map (bounded by tenant count),
// entries here are keyed by token hash — unbounded, since every login mints
// a new one. A background goroutine sweeps expired entries once a minute so
// a token that's never seen again still gets reclaimed instead of leaking
// forever; Close stops that goroutine and must be called during graceful
// shutdown.
type MemoryCache struct {
	mu      sync.Mutex
	entries map[string]cacheRow
	stop    chan struct{}
}

// NewMemoryCache starts the background sweep goroutine immediately — call
// Close when done with it (see cmd/server/main.go's shutdown path).
func NewMemoryCache() *MemoryCache {
	c := &MemoryCache{
		entries: make(map[string]cacheRow),
		stop:    make(chan struct{}),
	}
	go c.sweepLoop()
	return c
}

func (c *MemoryCache) sweepLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.sweep()
		case <-c.stop:
			return
		}
	}
}

func (c *MemoryCache) sweep() {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, row := range c.entries {
		if now.After(row.expiresAt) {
			delete(c.entries, k)
		}
	}
}

func (c *MemoryCache) Get(_ context.Context, key string) (CacheEntry, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	row, ok := c.entries[key]
	if !ok || time.Now().After(row.expiresAt) {
		return CacheEntry{}, false, nil
	}
	return row.entry, true, nil
}

func (c *MemoryCache) Set(_ context.Context, key string, entry CacheEntry, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheRow{entry: entry, expiresAt: time.Now().Add(ttl)}
	return nil
}

// Close stops the background sweep goroutine. Safe to call once.
func (c *MemoryCache) Close() {
	close(c.stop)
}

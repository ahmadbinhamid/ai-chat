package auth

import (
	"context"
	"sync"
	"time"

	"ai-chat/internal/safego"
)

// CacheEntry is a cached introspection result, never a resolved Identity
// (see package doc). Negative marks a cached 401.
type CacheEntry struct {
	UserID          uint64
	Name            string
	Email           string
	IsActive        bool
	Tenants         []Tenant
	DefaultTenantID *uint64
	Negative        bool
}

// Cache stores CacheEntry values behind hashed-token keys with independent TTLs.
// ctx/error exist so a future Redis-backed impl is a drop-in; a cache error must degrade to a live call, never a 500.
type Cache interface {
	Get(ctx context.Context, key string) (entry CacheEntry, ok bool, err error)
	Set(ctx context.Context, key string, entry CacheEntry, ttl time.Duration) error
}

type cacheRow struct {
	entry     CacheEntry
	expiresAt time.Time
}

// MemoryCache is an in-process TTL cache, valid only within a single instance. A background
// goroutine sweeps expired entries once a minute since the map is otherwise unbounded; Close must be called on shutdown.
type MemoryCache struct {
	mu      sync.Mutex
	entries map[string]cacheRow
	stop    chan struct{}
}

// NewMemoryCache starts the background sweep goroutine immediately; call Close when done.
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
			// Wrapped per-tick so one bad sweep doesn't stop future sweeps.
			func() {
				defer safego.Recover("auth.MemoryCache.sweep")
				c.sweep()
			}()
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

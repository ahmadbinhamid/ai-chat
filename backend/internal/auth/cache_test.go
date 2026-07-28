package auth

import (
	"context"
	"testing"
	"time"
)

func TestMemoryCache_SetThenGetWithinTTL(t *testing.T) {
	c := NewMemoryCache()
	defer c.Close()
	ctx := context.Background()

	entry := CacheEntry{UserID: 2, IsActive: true}
	if err := c.Set(ctx, "auth:abc", entry, time.Minute); err != nil {
		t.Fatalf("Set returned an error: %v", err)
	}

	got, ok, err := c.Get(ctx, "auth:abc")
	if err != nil {
		t.Fatalf("Get returned an error: %v", err)
	}
	if !ok {
		t.Fatal("expected a cache hit within the TTL")
	}
	if got.UserID != 2 {
		t.Fatalf("expected UserID 2, got %d", got.UserID)
	}
}

func TestMemoryCache_ExpiresAfterTTL(t *testing.T) {
	c := NewMemoryCache()
	defer c.Close()
	ctx := context.Background()

	_ = c.Set(ctx, "auth:abc", CacheEntry{UserID: 2}, 10*time.Millisecond)
	time.Sleep(30 * time.Millisecond)

	_, ok, err := c.Get(ctx, "auth:abc")
	if err != nil {
		t.Fatalf("Get returned an error: %v", err)
	}
	if ok {
		t.Fatal("expected the entry to have expired")
	}
}

func TestMemoryCache_MissReturnsFalseNotError(t *testing.T) {
	c := NewMemoryCache()
	defer c.Close()

	_, ok, err := c.Get(context.Background(), "auth:never-set")
	if err != nil {
		t.Fatalf("expected no error on a plain miss, got: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for a key that was never set")
	}
}

func TestMemoryCache_SweepReclaimsExpiredEntries(t *testing.T) {
	c := NewMemoryCache()
	defer c.Close()
	ctx := context.Background()

	_ = c.Set(ctx, "auth:expired", CacheEntry{UserID: 1}, time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	c.sweep()

	c.mu.Lock()
	_, stillThere := c.entries["auth:expired"]
	c.mu.Unlock()
	if stillThere {
		t.Fatal("expected sweep to have reclaimed the expired entry")
	}
}

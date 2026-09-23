package themebuild

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// Locking a huge number of distinct keys must never grow storage past the fixed stripe count.
func TestStripedMutex_MemoryBoundedRegardlessOfKeyCount(t *testing.T) {
	s := newStripedMutex(8)
	for i := 0; i < 10_000; i++ {
		unlock, err := s.Lock(context.Background(), fmt.Sprintf("chat-%d", i))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		unlock()
	}
	if len(s.stripes) != 8 {
		t.Fatalf("expected the stripe count to stay fixed at 8 regardless of key count, got %d", len(s.stripes))
	}
}

// Two concurrent Lock calls for the SAME key must never both be in the critical section at once.
func TestStripedMutex_SerializesTheSameKey(t *testing.T) {
	s := newStripedMutex(8)
	var inCriticalSection atomic.Int32
	var sawOverlap atomic.Bool
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := s.Lock(context.Background(), "same-chat-id")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if inCriticalSection.Add(1) > 1 {
				sawOverlap.Store(true)
			}
			inCriticalSection.Add(-1)
			unlock()
		}()
	}
	wg.Wait()

	if sawOverlap.Load() {
		t.Fatal("expected Lock to serialize concurrent callers on the same key, but two overlapped")
	}
}

// themeLockKey must differ per tenant for the same slug, but stay stable for the same (tenant, slug) pair.
func TestThemeLockKey_DifferentTenantsNeverCollide(t *testing.T) {
	a := themeLockKey(1, "shop")
	b := themeLockKey(2, "shop")
	if a == b {
		t.Fatalf("expected different tenants with the same slug to produce different lock keys, got %q for both", a)
	}
	if got := themeLockKey(1, "shop"); got != a {
		t.Errorf("expected the same (tenant, slug) pair to always produce the same key, got %q and %q", a, got)
	}
}

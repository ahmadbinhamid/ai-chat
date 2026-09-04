package themebuild

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// TestStripedMutex_MemoryBoundedRegardlessOfKeyCount is the fix this type
// exists for: historySummaryLocks used to grow one permanent *sync.Mutex
// entry per distinct chat ID for the life of the process (see
// stripedMutex's own doc comment) — locking a huge number of distinct keys
// must never grow the underlying storage past the fixed stripe count.
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

// TestStripedMutex_SerializesTheSameKey confirms the actual locking
// guarantee still holds: two concurrent Lock calls for the SAME key must
// never both be "in the critical section" at once.
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

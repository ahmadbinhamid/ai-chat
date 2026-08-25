package safego

import (
	"sync"
	"testing"
)

func TestRecover_StopsAPanicFromPropagating(t *testing.T) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)

	// If Recover didn't work, this panic would crash the test binary
	// instead of just failing this one test.
	go func() {
		defer wg.Done()
		defer Recover("test")
		defer close(done)
		panic("boom")
	}()

	wg.Wait()
	select {
	case <-done:
		// Reaching here at all proves the panic didn't propagate.
	default:
		t.Fatal("expected the deferred close(done) to run despite the panic")
	}
}

func TestRecover_NoOpWhenNothingPanicked(t *testing.T) {
	ran := false
	func() {
		defer Recover("test")
		ran = true
	}()
	if !ran {
		t.Error("expected the function body to run normally when nothing panics")
	}
}

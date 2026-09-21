package themebuild

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestWallClockStatus(t *testing.T) {
	t.Parallel()

	if got := wallClockStatus(nil, nil); got != "completed" {
		t.Fatalf("nil err: got %q", got)
	}
	if got := wallClockStatus(errors.New("boom"), nil); got != "failed" {
		t.Fatalf("generic err: got %q", got)
	}
	if got := wallClockStatus(context.DeadlineExceeded, nil); got != "timed_out" {
		t.Fatalf("deadline: got %q", got)
	}
	if got := wallClockStatus(context.Canceled, nil); got != "cancelled" {
		t.Fatalf("canceled: got %q", got)
	}
	var cancelled atomic.Bool
	cancelled.Store(true)
	if got := wallClockStatus(errors.New("boom"), &cancelled); got != "cancelled" {
		t.Fatalf("merchant cancel wins: got %q", got)
	}
}

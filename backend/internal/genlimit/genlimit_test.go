package genlimit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTryAcquire_Caps(t *testing.T) {
	tests := []struct {
		name      string
		global    int
		perTenant int
		// tenants acquire in order; want is whether each one gets a slot.
		tenants []uint64
		want    []bool
	}{
		{"global cap", 2, 0, []uint64{1, 2, 3}, []bool{true, true, false}},
		{"tenant cap", 0, 2, []uint64{1, 1, 1, 2}, []bool{true, true, false, true}},
		{"both caps, tenant binds first", 3, 1, []uint64{1, 1, 2, 3, 4}, []bool{true, false, true, true, false}},
		{"zero is unlimited", 0, 0, []uint64{1, 1, 1, 1, 1}, []bool{true, true, true, true, true}},
		{"negative is unlimited", -1, -1, []uint64{1, 1, 1}, []bool{true, true, true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := New(tt.global, tt.perTenant)
			for i, tenant := range tt.tenants {
				if _, ok := l.TryAcquire(tenant); ok != tt.want[i] {
					t.Fatalf("acquire %d (tenant %d) = %v, want %v", i, tenant, ok, tt.want[i])
				}
			}
		})
	}
}

func TestRelease_FreesSlotAndShrinksMap(t *testing.T) {
	tests := []struct {
		name    string
		tenants []uint64
	}{
		{"one tenant", []uint64{7}},
		{"same tenant twice", []uint64{7, 7}},
		{"many tenants", []uint64{1, 2, 3, 4, 5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := New(0, 0)
			var releases []func()
			for _, tenant := range tt.tenants {
				release, ok := l.TryAcquire(tenant)
				if !ok {
					t.Fatalf("unlimited acquire failed")
				}
				releases = append(releases, release)
			}
			for _, release := range releases {
				release()
				release() // idempotent: a second call must not free another slot
			}
			if total, _ := l.Running(tt.tenants[0]); total != 0 {
				t.Errorf("running = %d after releasing everything, want 0", total)
			}
			if n := l.trackedTenants(); n != 0 {
				t.Errorf("tenant map has %d entries after every release, want 0", n)
			}
		})
	}
}

func TestAcquire_WaitsForRelease(t *testing.T) {
	tests := []struct {
		name          string
		global        int
		perTenant     int
		holder, waits uint64
	}{
		{"global cap", 1, 0, 1, 2},
		{"tenant cap", 0, 1, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := New(tt.global, tt.perTenant)
			release, ok := l.TryAcquire(tt.holder)
			if !ok {
				t.Fatal("first acquire failed")
			}
			got := make(chan error, 1)
			go func() {
				r, err := l.Acquire(context.Background(), tt.waits)
				if err == nil {
					r()
				}
				got <- err
			}()
			select {
			case err := <-got:
				t.Fatalf("Acquire returned %v before any slot was released", err)
			case <-time.After(50 * time.Millisecond):
			}
			release()
			select {
			case err := <-got:
				if err != nil {
					t.Fatalf("Acquire after release = %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Acquire never woke after release")
			}
		})
	}
}

func TestAcquire_CancelWhileWaiting(t *testing.T) {
	l := New(1, 0)
	release, _ := l.TryAcquire(1)
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan error, 1)
	go func() {
		_, err := l.Acquire(ctx, 2)
		got <- err
	}()
	cancel()
	select {
	case err := <-got:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Acquire = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Acquire ignored cancellation")
	}
	if total, _ := l.Running(2); total != 1 {
		t.Errorf("running = %d, want 1: a cancelled wait must not take a slot", total)
	}
	if n := l.trackedTenants(); n != 1 {
		t.Errorf("tenant map has %d entries, want 1: a cancelled wait must not add one", n)
	}
}

func TestRelease_SurvivesPanic(t *testing.T) {
	l := New(1, 1)
	func() {
		defer func() { _ = recover() }()
		release, ok := l.TryAcquire(9)
		if !ok {
			t.Fatal("acquire failed")
		}
		defer release()
		panic("generation blew up")
	}()
	if _, ok := l.TryAcquire(9); !ok {
		t.Fatal("slot was not released by the deferred release during the panic")
	}
}

func TestNilLimiter_IsUnlimited(t *testing.T) {
	var l *Limiter
	release, ok := l.TryAcquire(1)
	if !ok {
		t.Fatal("nil limiter refused a slot")
	}
	release()
	release, err := l.Acquire(context.Background(), 1)
	if err != nil {
		t.Fatalf("nil limiter Acquire = %v", err)
	}
	release()
}

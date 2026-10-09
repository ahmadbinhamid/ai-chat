// Package genlimit caps how many AI generations run at once, globally and per tenant. Limits are per process.
package genlimit

import (
	"context"
	"sync"
)

// Limiter is safe for concurrent use. A nil *Limiter, or a limit of 0, is unlimited.
type Limiter struct {
	global    int
	perTenant int

	mu      sync.Mutex
	running int
	// Entries are deleted at zero, so the map holds only tenants with a generation running right now.
	tenants map[uint64]int
	// Closed and replaced on every release, waking all waiters to re-check.
	freed chan struct{}
}

func New(global, perTenant int) *Limiter {
	return &Limiter{
		global:    max(global, 0),
		perTenant: max(perTenant, 0),
		tenants:   make(map[uint64]int),
		freed:     make(chan struct{}),
	}
}

// TryAcquire takes a slot for tenantID without waiting. The returned release is idempotent.
func (l *Limiter) TryAcquire(tenantID uint64) (release func(), ok bool) {
	if l == nil {
		return func() {}, true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.fitsLocked(tenantID) {
		return nil, false
	}
	return l.takeLocked(tenantID), true
}

// Acquire waits for a slot for tenantID until ctx is done. The returned release is idempotent.
func (l *Limiter) Acquire(ctx context.Context, tenantID uint64) (release func(), err error) {
	if l == nil {
		return func() {}, nil
	}
	for {
		l.mu.Lock()
		if l.fitsLocked(tenantID) {
			release = l.takeLocked(tenantID)
			l.mu.Unlock()
			return release, nil
		}
		freed := l.freed
		l.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-freed:
		}
	}
}

// Running reports the generations holding a slot overall and for tenantID.
func (l *Limiter) Running(tenantID uint64) (total, tenant int) {
	if l == nil {
		return 0, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.running, l.tenants[tenantID]
}

// trackedTenants is the per-tenant map's size; tests assert it shrinks back to zero.
func (l *Limiter) trackedTenants() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.tenants)
}

func (l *Limiter) fitsLocked(tenantID uint64) bool {
	if l.global > 0 && l.running >= l.global {
		return false
	}
	return l.perTenant == 0 || l.tenants[tenantID] < l.perTenant
}

func (l *Limiter) takeLocked(tenantID uint64) func() {
	l.running++
	l.tenants[tenantID]++
	var once sync.Once
	return func() { once.Do(func() { l.release(tenantID) }) }
}

func (l *Limiter) release(tenantID uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.running--
	if n := l.tenants[tenantID] - 1; n > 0 {
		l.tenants[tenantID] = n
	} else {
		delete(l.tenants, tenantID)
	}
	close(l.freed)
	l.freed = make(chan struct{})
}

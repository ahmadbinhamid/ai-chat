package ratelimit

import "testing"

// TestPerTenantLimiter_BurstThenDeny covers the doc comment's own claim:
// burst equals ratePerMin, so a tenant can spend their whole minute's
// allowance immediately, and the very next request past that must be
// denied rather than silently allowed through.
func TestPerTenantLimiter_BurstThenDeny(t *testing.T) {
	l := NewPerTenantLimiter(5)

	for i := range 5 {
		if !l.Allow(1) {
			t.Fatalf("expected request %d (within the burst of 5) to be allowed", i+1)
		}
	}

	if l.Allow(1) {
		t.Fatal("expected the 6th request to be denied — it exceeds the burst")
	}
}

// TestPerTenantLimiter_PerTenantIsolation covers the "per-tenant" half of
// PerTenantLimiter's name: one tenant exhausting their own allowance must
// never affect a different tenant's independent bucket.
func TestPerTenantLimiter_PerTenantIsolation(t *testing.T) {
	l := NewPerTenantLimiter(1)

	if !l.Allow(1) {
		t.Fatal("expected tenant 1's first request to be allowed")
	}
	if l.Allow(1) {
		t.Fatal("expected tenant 1's second request to be denied — burst of 1 already spent")
	}

	if !l.Allow(2) {
		t.Fatal("expected tenant 2's first request to be allowed — a different tenant's bucket must be untouched by tenant 1's usage")
	}
}

// TestNewPerTenantLimiter_ClampsNonPositiveRate covers the constructor's
// own guard: a caller passing 0 or a negative GENERATION_RATE_LIMIT_PER_MINUTE
// must still get a usable (if maximally strict) limiter, not one that
// permits everything (rate.Limit(0) would otherwise mean "unlimited" is
// never reached, not "always allow") or panics.
func TestNewPerTenantLimiter_ClampsNonPositiveRate(t *testing.T) {
	l := NewPerTenantLimiter(0)

	if !l.Allow(1) {
		t.Fatal("expected the first request to still be allowed after clamping to a rate of at least 1/min")
	}
	if l.Allow(1) {
		t.Fatal("expected the second request to be denied — clamped burst is 1")
	}
}

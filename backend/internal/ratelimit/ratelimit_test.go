package ratelimit

import "testing"

// TestPerTenantLimiter_BurstThenDeny checks burst equals ratePerMin and the next request over it is denied.
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

// TestPerTenantLimiter_PerTenantIsolation checks one tenant's exhausted bucket doesn't affect another's.
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

// TestPerTenantLimiter_BoundedByMaxTenants checks the map never grows past ratelimitMaxTenants.
func TestPerTenantLimiter_BoundedByMaxTenants(t *testing.T) {
	l := NewPerTenantLimiter(5)

	for i := range uint64(ratelimitMaxTenants + 100) {
		l.Allow(i)
		if len(l.limiters) > ratelimitMaxTenants {
			t.Fatalf("limiters map grew to %d entries after tenant %d, want at most %d", len(l.limiters), i, ratelimitMaxTenants)
		}
	}

	if len(l.limiters) != ratelimitMaxTenants {
		t.Fatalf("expected the map to sit exactly at its cap of %d once more than that many tenants have been seen, got %d", ratelimitMaxTenants, len(l.limiters))
	}
}

// TestNewPerTenantLimiter_ClampsNonPositiveRate checks a 0/negative rate clamps to a
// usable strict limiter instead of meaning "unlimited" or panicking.
func TestNewPerTenantLimiter_ClampsNonPositiveRate(t *testing.T) {
	l := NewPerTenantLimiter(0)

	if !l.Allow(1) {
		t.Fatal("expected the first request to still be allowed after clamping to a rate of at least 1/min")
	}
	if l.Allow(1) {
		t.Fatal("expected the second request to be denied — clamped burst is 1")
	}
}

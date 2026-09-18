package ai

import (
	"context"
	"testing"
	"time"
)

func TestFirstTokenTimeoutForMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mode FirstTokenMode
		want time.Duration
	}{
		{FirstTokenModeSimple, 45 * time.Second},
		{FirstTokenModeSection, 75 * time.Second},
		{FirstTokenModeFullPage, 120 * time.Second},
		{FirstTokenModeComplex, 180 * time.Second},
		{FirstTokenMode("unknown"), 45 * time.Second},
	}
	for _, tc := range cases {
		got := FirstTokenTimeoutForMode(tc.mode)
		if got != tc.want {
			t.Fatalf("mode=%s got %v want %v", tc.mode, got, tc.want)
		}
		if got > MaxFirstTokenTimeout {
			t.Fatalf("mode=%s exceeds max: %v", tc.mode, got)
		}
	}
	if PreparedFirstTokenTimeout() != FirstTokenTimeoutComplex {
		t.Fatalf("PreparedFirstTokenTimeout=%v want %v", PreparedFirstTokenTimeout(), FirstTokenTimeoutComplex)
	}
	if PreparedFullPageFirstTokenTimeout() != FirstTokenTimeoutFullPage {
		t.Fatalf("PreparedFullPageFirstTokenTimeout=%v want %v", PreparedFullPageFirstTokenTimeout(), FirstTokenTimeoutFullPage)
	}
}

func TestClampFirstTokenTimeout(t *testing.T) {
	t.Parallel()
	if got := ClampFirstTokenTimeout(0); got != FirstTokenTimeoutSimpleEdit {
		t.Fatalf("zero got %v", got)
	}
	if got := ClampFirstTokenTimeout(5 * time.Minute); got != MaxFirstTokenTimeout {
		t.Fatalf("over-max got %v want %v", got, MaxFirstTokenTimeout)
	}
	if got := ClampFirstTokenTimeout(90 * time.Second); got != 90*time.Second {
		t.Fatalf("in-range got %v", got)
	}
}

func TestClampFirstTokenToParent(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	got := ClampFirstTokenToParent(ctx, FirstTokenTimeoutComplex)
	if got > 20*time.Second || got <= 0 {
		t.Fatalf("parent clamp got %v (want ≤20s)", got)
	}
	// No deadline → mode budget (capped).
	got = ClampFirstTokenToParent(context.Background(), FirstTokenTimeoutComplex)
	if got != FirstTokenTimeoutComplex {
		t.Fatalf("no-deadline got %v want %v", got, FirstTokenTimeoutComplex)
	}
}

func TestSimpleEditFirstTokenAt44sBudget(t *testing.T) {
	t.Parallel()
	// Regression: simple_edit budget is 45s — 44s is within budget.
	if FirstTokenTimeoutSimpleEdit <= 44*time.Second {
		t.Fatalf("simple_edit TTFT too aggressive: %v", FirstTokenTimeoutSimpleEdit)
	}
	if FirstTokenTimeoutSimpleEdit > 45*time.Second {
		t.Fatalf("simple_edit TTFT too loose: %v", FirstTokenTimeoutSimpleEdit)
	}
}

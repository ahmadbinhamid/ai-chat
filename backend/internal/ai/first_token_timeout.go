package ai

import (
	"context"
	"time"
)

// Mode-aware first-token (TTFT) budgets. Child of the parent generation
// deadline (10 minutes) — never unlimited, never above MaxFirstTokenTimeout.
const (
	FirstTokenTimeoutSimpleEdit  = 45 * time.Second
	FirstTokenTimeoutSectionEdit = 75 * time.Second // mid of 60–90s band
	FirstTokenTimeoutFullPage    = 120 * time.Second
	FirstTokenTimeoutComplex     = 180 * time.Second
	MaxFirstTokenTimeout         = 180 * time.Second
	minFirstTokenRetryBudget     = 5 * time.Second
)

// FirstTokenMode selects a bounded TTFT budget for a generation class.
type FirstTokenMode string

const (
	FirstTokenModeSimple   FirstTokenMode = "simple_edit"
	FirstTokenModeSection  FirstTokenMode = "section_edit"
	FirstTokenModeFullPage FirstTokenMode = "full_page"
	FirstTokenModeComplex  FirstTokenMode = "complex" // complex_page + compound
)

// FirstTokenTimeoutForMode returns the capped TTFT budget for mode.
func FirstTokenTimeoutForMode(mode FirstTokenMode) time.Duration {
	switch mode {
	case FirstTokenModeSimple:
		return FirstTokenTimeoutSimpleEdit
	case FirstTokenModeSection:
		return FirstTokenTimeoutSectionEdit
	case FirstTokenModeFullPage:
		return FirstTokenTimeoutFullPage
	case FirstTokenModeComplex:
		return FirstTokenTimeoutComplex
	default:
		return FirstTokenTimeoutSimpleEdit
	}
}

// ClampFirstTokenTimeout caps d to MaxFirstTokenTimeout. Zero/negative
// returns the simple-edit default.
func ClampFirstTokenTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return FirstTokenTimeoutSimpleEdit
	}
	if d > MaxFirstTokenTimeout {
		return MaxFirstTokenTimeout
	}
	return d
}

// ClampFirstTokenToParent caps the TTFT child deadline so it cannot outlive
// the parent generation context. When the parent has no deadline, only the
// absolute max applies.
func ClampFirstTokenToParent(ctx context.Context, d time.Duration) time.Duration {
	d = ClampFirstTokenTimeout(d)
	if ctx == nil {
		return d
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return d
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return minFirstTokenRetryBudget
	}
	if d > remaining {
		return remaining
	}
	return d
}

package themebuild

import (
	"context"
	"errors"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/themecheck"
)

func TestShouldStartRepairGenerate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		ctx        context.Context
		attempt    int
		maxRetries int
		errorCount int
		wantOK     bool
		wantReason RepairSkipReason
	}{
		{
			name: "validation succeeds — no repair",
			ctx:  context.Background(), attempt: 1, maxRetries: 2, errorCount: 0,
			wantOK: false, wantReason: RepairSkipNoErrors,
		},
		{
			name: "repairable validation failure",
			ctx:  context.Background(), attempt: 1, maxRetries: 2, errorCount: 3,
			wantOK: true, wantReason: RepairSkipNone,
		},
		{
			name: "last allowed repair attempt",
			ctx:  context.Background(), attempt: 2, maxRetries: 2, errorCount: 1,
			wantOK: true, wantReason: RepairSkipNone,
		},
		{
			name: "repair attempt limit reached",
			ctx:  context.Background(), attempt: 3, maxRetries: 2, errorCount: 1,
			wantOK: false, wantReason: RepairSkipBudgetExhausted,
		},
		{
			name: "context canceled",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			}(),
			attempt: 1, maxRetries: 2, errorCount: 1,
			wantOK: false, wantReason: RepairSkipContextCanceled,
		},
		{
			name: "deadline exceeded",
			ctx: func() context.Context {
				ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
				defer cancel()
				time.Sleep(time.Millisecond)
				return ctx
			}(),
			attempt: 1, maxRetries: 2, errorCount: 1,
			wantOK: false, wantReason: RepairSkipDeadlineExceeded,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ok, reason := shouldStartRepairGenerate(tt.ctx, tt.attempt, tt.maxRetries, tt.errorCount)
			if ok != tt.wantOK || reason != tt.wantReason {
				t.Fatalf("got ok=%v reason=%q want ok=%v reason=%q", ok, reason, tt.wantOK, tt.wantReason)
			}
		})
	}
}

func TestPrepareRepairThemeContext_StripsFullPageOverrides(t *testing.T) {
	t.Parallel()
	tc := ai.ThemeContext{
		ThemeSlug:                 "demo",
		GenerationMode:            ai.GenerationModePages,
		MaxTokensOverride:         24_000,
		MaxToolIterations:         28,
		MaxExplorationToolCalls:   40,
		StreamMaxAttemptsOverride: 4,
		FirstTokenTimeoutOverride: 2 * time.Minute,
		SimpleEditOneShot:         true,
		PageCreatePrepared:        true,
		FullHomeRedesign:          true,
		EffortOverride:            "xhigh",
	}
	got := prepareRepairThemeContext(tc)
	if !got.Repair {
		t.Fatal("expected Repair=true")
	}
	if got.MaxTokensOverride != 0 {
		t.Fatalf("MaxTokensOverride must clear for repair budget, got %d", got.MaxTokensOverride)
	}
	if got.MaxToolIterations != 0 {
		t.Fatalf("MaxToolIterations must clear, got %d", got.MaxToolIterations)
	}
	if got.StreamMaxAttemptsOverride != 0 || got.FirstTokenTimeoutOverride != 0 {
		t.Fatal("stream overrides must clear for repair")
	}
	if got.SimpleEditOneShot || got.PageCreatePrepared || got.FullHomeRedesign {
		t.Fatal("full-page flags must clear")
	}
	if got.EffortOverride != "medium" {
		t.Fatalf("effort=%q", got.EffortOverride)
	}
	if got.FileTree != nil || got.Manifest != nil {
		t.Fatal("tree/manifest must be nil for focused repair")
	}
}
func TestRepairReasonCategory(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   []themecheck.Finding
		want RepairReasonCategory
	}{
		{"empty", nil, RepairReasonNone},
		{"schema", []themecheck.Finding{{Rule: "allowed-syntax", Severity: themecheck.SeverityError}}, RepairReasonSchemaError},
		{"required", []themecheck.Finding{{Rule: "page-boilerplate", Severity: themecheck.SeverityError}}, RepairReasonRequiredFile},
		{"themecheck", []themecheck.Finding{{Rule: "theme-token", Severity: themecheck.SeverityError}}, RepairReasonThemecheckError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := repairReasonCategory(tt.in); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestAmplificationBounds_Documented(t *testing.T) {
	t.Parallel()
	// Deterministic policy ceiling for one user generation (excluding
	// per-iteration provider stream retries inside each Generate):
	//   initial Generate ≤ maxThemeCheckRetries+1
	//   repair Generate  ≤ maxThemeCheckRetries
	//   provider stream attempts per iteration ≤ streamAccumulateMaxAttempts (2)
	if maxInitialProposalGenerateCalls() != maxThemeCheckRetries+1 {
		t.Fatalf("initial bound %d", maxInitialProposalGenerateCalls())
	}
	if maxRepairGenerateCalls() != maxThemeCheckRetries {
		t.Fatalf("repair bound %d", maxRepairGenerateCalls())
	}
	maxGenerateCalls := maxInitialProposalGenerateCalls() + maxRepairGenerateCalls()
	if maxGenerateCalls != 5 {
		t.Fatalf("expected max 5 Generate calls under default policy, got %d", maxGenerateCalls)
	}
}

func TestIsTransientRepairErr_NotAuth(t *testing.T) {
	t.Parallel()
	if isTransientRepairErr(errors.New("401 unauthorized")) {
		t.Fatal("auth must not be treated as keep-prior transient")
	}
	if !isTransientRepairErr(context.DeadlineExceeded) {
		t.Fatal("deadline should be transient for keep-prior")
	}
}

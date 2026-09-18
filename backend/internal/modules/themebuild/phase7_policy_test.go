package themebuild

import (
	"testing"

	"ai-chat/internal/prodhardening"
)

// TestPolicyMatchesRuntime keeps prodhardening.DefaultPolicy aligned with
// the real compiled constants (Phase 7).
func TestPolicyMatchesRuntime(t *testing.T) {
	t.Parallel()
	p := prodhardening.DefaultPolicy()
	if p.MaxQueueDepthPerChat != maxQueueDepth {
		t.Fatalf("queue depth policy=%d runtime=%d", p.MaxQueueDepthPerChat, maxQueueDepth)
	}
	if p.MaxThemeCheckRetries != maxThemeCheckRetries {
		t.Fatalf("retries policy=%d runtime=%d", p.MaxThemeCheckRetries, maxThemeCheckRetries)
	}
	if p.MaxInitialProposalGenerate != maxInitialProposalGenerateCalls() {
		t.Fatalf("initial generate policy=%d runtime=%d", p.MaxInitialProposalGenerate, maxInitialProposalGenerateCalls())
	}
	if p.MaxRepairGenerate != maxRepairGenerateCalls() {
		t.Fatalf("repair policy=%d runtime=%d", p.MaxRepairGenerate, maxRepairGenerateCalls())
	}
	if p.MaxGrepFilesScanned != maxGrepFilesScanned {
		t.Fatalf("grep files policy=%d runtime=%d", p.MaxGrepFilesScanned, maxGrepFilesScanned)
	}
	if p.MaxGrepMatches != maxGrepMatches {
		t.Fatalf("grep matches policy=%d runtime=%d", p.MaxGrepMatches, maxGrepMatches)
	}
	if p.GrepMaxConcurrency != loadThemeFilesConcurrency {
		t.Fatalf("grep concurrency policy=%d runtime=%d", p.GrepMaxConcurrency, loadThemeFilesConcurrency)
	}
	if p.SubscriberBufferSize != subscriberBufferSize {
		t.Fatalf("bus buffer policy=%d runtime=%d", p.SubscriberBufferSize, subscriberBufferSize)
	}
	if p.MaxGenerationEventsPerChat != maxGenerationEventsPerChat {
		t.Fatalf("events policy=%d runtime=%d", p.MaxGenerationEventsPerChat, maxGenerationEventsPerChat)
	}
	if int64(p.GenerationTimeout) != generateTimeoutNanos.Load() {
		t.Fatalf("generate timeout mismatch")
	}
	if p.ThemeCacheMaxEntries != 512 {
		t.Fatalf("cache entries policy=%d want 512", p.ThemeCacheMaxEntries)
	}
	if p.ThemeCacheMaxBytes != 512*40_000 {
		t.Fatalf("cache bytes policy=%d", p.ThemeCacheMaxBytes)
	}
}

func TestAmplificationBounds_WithEscalationAndStreamRetries(t *testing.T) {
	t.Parallel()
	p := prodhardening.DefaultPolicy()
	// Without escalation: 3+2=5 Generate calls (existing Phase 4 doc).
	base := maxInitialProposalGenerateCalls() + maxRepairGenerateCalls()
	if base != 5 {
		t.Fatalf("base Generate bound %d", base)
	}
	// With one simple-edit escalation: another full initial budget.
	withEsc := base + maxInitialProposalGenerateCalls()
	if withEsc != p.MaxGenerateCallsWithEscalation {
		t.Fatalf("escalation Generate bound %d want %d", withEsc, p.MaxGenerateCallsWithEscalation)
	}
	maxStreams := prodhardening.MaxProviderStreamAttemptsPerGeneration(p)
	if maxStreams != withEsc*p.MaxToolIterationsPerGenerate*p.StreamAccumulateMaxAttempts {
		t.Fatalf("stream attempt formula mismatch %d", maxStreams)
	}
}

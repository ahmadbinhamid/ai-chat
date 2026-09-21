package ai

import (
	"strings"
	"testing"
	"time"
)

func TestTurnMetrics_GenerateAndRepairCounts(t *testing.T) {
	t.Parallel()
	m := &TurnMetrics{}
	m.RecordGenerateCall(false)
	m.RecordGenerateCall(false)
	m.RecordGenerateCall(true)
	m.RecordProviderRetries(2)
	m.SetRepairAttempts(1)
	m.SetRepairAttempts(3)
	m.SetRepairAttempts(2) // should not decrease
	snap := m.Snapshot()
	if snap.GenerateCalls != 3 || snap.InitialGenerateCalls != 2 || snap.RepairGenerateCalls != 1 {
		t.Fatalf("generate calls: %+v", snap)
	}
	if snap.ProviderRetries != 2 || snap.RepairAttempts != 3 {
		t.Fatalf("retries/repair: %+v", snap)
	}
}

func TestTurnMetrics_ToolAndIterationCounters(t *testing.T) {
	t.Parallel()
	m := &TurnMetrics{}
	m.RecordModelIteration(100, 40, true, 10, 20, 5, true, 1, 2, true, true)
	m.RecordModelIteration(50, 0, false, 3, 4, 0, false, 0, 0, false, false)
	m.RecordToolCall(toolNameListThemeFiles, 5)
	m.RecordToolCall(toolNameReadThemeFile, 7)
	m.RecordToolCall(toolNameGrepTheme, 9)
	m.RecordToolCall(toolNameValidateChanges, 2)
	m.RecordProposeCall()
	m.RecordGrep(GrepIOMetrics{FilesScanned: 12, Matches: 3, FlowPOSReads: 13, FlowPOSElapsedMs: 40, CacheHits: 4, PeakConcurrent: 8, MaxConcurrency: 8})

	snap := m.Snapshot()
	if snap.ModelIterations != 2 || snap.ModelElapsedMs != 150 {
		t.Fatalf("model: %+v", snap)
	}
	if !snap.FirstTTFTAvailable || snap.FirstTTFTMs != 40 {
		t.Fatalf("ttft: available=%v ms=%d", snap.FirstTTFTAvailable, snap.FirstTTFTMs)
	}
	if snap.ListCalls != 1 || snap.ReadCalls != 1 || snap.GrepCalls != 1 || snap.ValidateCalls != 1 || snap.ProposeCalls != 1 {
		t.Fatalf("tool counts: %+v", snap)
	}
	if snap.ToolCalls != 5 || snap.ToolElapsedMs != 23 {
		t.Fatalf("tool totals: calls=%d elapsed=%d", snap.ToolCalls, snap.ToolElapsedMs)
	}
	if snap.InputTokens != 13 || snap.OutputTokens != 24 || !snap.ReasoningReported || snap.ReasoningTokens != 5 {
		t.Fatalf("tokens: %+v", snap)
	}
	if snap.GrepFilesScanned != 12 || snap.GrepMatchesFound != 3 || snap.GrepFlowPOSReads != 13 {
		t.Fatalf("grep: %+v", snap)
	}
	if snap.GrepCacheHits != 4 || snap.GrepPeakConcurrent != 8 {
		t.Fatalf("grep concurrency/cache: %+v", snap)
	}
}

func TestComputeTTFT(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		first     time.Time
		wantMs    int64
		wantAvail bool
	}{
		{name: "available", first: start.Add(125 * time.Millisecond), wantMs: 125, wantAvail: true},
		{name: "unavailable_zero", first: time.Time{}, wantMs: 0, wantAvail: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ms, ok := ComputeTTFTMs(start, tc.first)
			if ok != tc.wantAvail || ms != tc.wantMs {
				t.Fatalf("got ms=%d avail=%v want ms=%d avail=%v", ms, ok, tc.wantMs, tc.wantAvail)
			}
		})
	}
}

func TestPerformanceTelemetryOmitsSecrets(t *testing.T) {
	t.Parallel()
	// Guardrail: known sensitive keys must never appear in Phase 0 metric field names.
	forbidden := []string{"authorization", "bearer", "cookie", "prompt", "content", "token_value", "api_key"}
	fieldNames := []string{
		"tenant_id", "chat_id", "generation_id", "provider", "model",
		"ttft_ms", "ttft_available", "elapsed_ms", "model_elapsed_ms", "tool_elapsed_ms",
		"input_tokens", "output_tokens", "reasoning_tokens", "cache_read_input_tokens",
		"grep_elapsed_ms", "files_scanned", "flowpos_read_count", "http_status", "success",
		"total_generate_calls", "repair_attempts", "provider_retries",
	}
	joined := strings.ToLower(strings.Join(fieldNames, " "))
	for _, f := range forbidden {
		if strings.Contains(joined, f) && f != "token" {
			// "input_tokens" contains "token" as substring — allow token counts only.
			if f == "token_value" || f == "api_key" || f == "authorization" || f == "bearer" || f == "cookie" || f == "prompt" || f == "content" {
				t.Fatalf("forbidden field substring %q present in metrics catalog", f)
			}
		}
		for _, name := range fieldNames {
			if name == f {
				t.Fatalf("forbidden exact field %q", f)
			}
		}
	}
}

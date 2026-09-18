package themebuild

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"ai-chat/internal/ai"
)

func TestLogGrepMetrics_Fields(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	m := &ai.TurnMetrics{}
	logGrepMetrics(7, m, time.Now().Add(-50*time.Millisecond), ai.GrepIOMetrics{
		FilesScanned:     40,
		Matches:          5,
		FlowPOSReads:     41,
		FlowPOSElapsedMs: 90,
		CacheHits:        10,
		CacheMisses:      2,
		PeakConcurrent:   8,
		MaxConcurrency:   8,
	})

	snap := m.Snapshot()
	if snap.GrepFilesScanned != 40 || snap.GrepMatchesFound != 5 || snap.GrepFlowPOSReads != 41 {
		t.Fatalf("snap=%+v", snap)
	}
	if snap.GrepCacheHits != 10 || snap.GrepPeakConcurrent != 8 || snap.GrepMaxConcurrency != 8 {
		t.Fatalf("concurrency/cache snap=%+v", snap)
	}
	out := buf.String()
	for _, want := range []string{
		"ai: grep_theme metrics", "files_scanned", "matches_found", "flowpos_read_count",
		"cache_hits", "cache_misses", "concurrent_reads", "max_concurrency",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("log missing %q: %s", want, out)
		}
	}
	for _, bad := range []string{"Authorization", "Bearer", "password"} {
		if strings.Contains(out, bad) {
			t.Fatalf("sensitive/unexpected %q in log: %s", bad, out)
		}
	}
}

func TestPerformanceSummaryAttrs_NoSecrets(t *testing.T) {
	t.Parallel()
	fields := []string{
		"tenant_id", "chat_id", "generation_id", "provider", "model", "status",
		"queue_wait_ms", "pre_model_ms", "ttft_ms", "model_elapsed_ms", "tool_elapsed_ms",
		"total_elapsed_ms", "total_generate_calls", "total_model_iterations", "total_tool_calls",
		"list_calls", "read_calls", "grep_calls", "validate_calls", "propose_calls",
		"repair_attempts", "provider_retries", "input_tokens", "output_tokens",
		"flowpos_list_files", "flowpos_read_file",
		"theme_cache_hits", "theme_cache_misses", "theme_cache_entries", "theme_cache_bytes",
		"list_files_cache_hits", "read_file_cache_hits",
		"repair_elapsed_ms", "provider_retry_wait_ms", "repair_budget_exhausted",
		"repair_skipped", "repair_skip_reason", "repair_reason",
	}
	joined := strings.Join(fields, ",")
	for _, bad := range []string{"authorization", "bearer", "prompt", "password", "cookie"} {
		if strings.Contains(strings.ToLower(joined), bad) {
			t.Fatalf("summary catalog contains %q", bad)
		}
	}
}

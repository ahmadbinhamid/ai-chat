// Package perfmetrics documents the structured fields emitted by
// ai.Generator and themebuild.Service for comparing provider latency.
// Live numbers come from process logs (ai: generate call finished,
// ai: model call timing, themebuild cache_hits / pre_model_ms /
// queue_wait_ms) — this package does not invent benchmarks.
//
// Phase 0 correlation keys on every major timing log:
//   tenant_id, chat_id, generation_id, provider, model
//
// Key log messages:
//   - ai: queue wait — queued_at, generation_started_at, queue_wait_ms
//   - ai: pre-model phase finished — draft_load_ms, workspace_sync_ms,
//     url_fetch_ms, theme_context_ms, intent_context_ms, snapshot_base_ms,
//     history_ms, pre_model_ms
//   - ai: model call timing — ttft_ms/ttft_available, tokens (*_available)
//   - ai: tool exec timing — tool, elapsed_ms, success
//   - ai: grep_theme metrics — files_scanned, matches_found, flowpos_*,
//     cache_hits, cache_misses, concurrent_reads, max_concurrency
//   - themefs: api request — operation, elapsed_ms, http_status, success
//   - ai: theme cache stats / generation summary — theme_cache_hits,
//     theme_cache_misses, theme_cache_entries, theme_cache_bytes,
//     list_files_cache_hits, read_file_cache_hits
//   - ai: generate call finished — model_elapsed_ms, tool_elapsed_ms, ttft_ms,
//     system_prompt_bytes, tool_schema_bytes, message_input_bytes,
//     zero_tool_nudges, forced_propose_count, propose_nudge_count
//   - ai: provider request shape — per-iteration input byte sizes (no content)
//   - ai: tool result size — result_bytes only (no tool output content)
//   - ai: check_and_repair finished — repair_attempts, repair_generate_calls,
//     repair_elapsed_ms, repair_skipped, repair_budget_exhausted
//   - ai: draft persist finished — elapsed_ms, file_count
//   - ai: generation wall-clock — total_elapsed_ms, status, timestamps
//   - ai: generation performance summary — aggregated Phase 0 + Phase 4 rollup
//     (initial_generate_calls, repair_generate_calls, provider_retries,
//     repair_elapsed_ms, provider_retry_wait_ms, repair_skip_reason)
//   - ai: websocket connected / disconnected — connection_duration_ms
//   - ai: terminal event emitted / duplicate terminal suppressed / stale event ignored
//
// Phase 6 process counters (ai-chat/internal/genlifecycle, not keyed by ID):
//   ConnectionAttempts, DisconnectCount, DuplicateEventsSuppressed,
//   StaleEventsIgnored, TerminalEventsEmitted, TerminalEventsSuppressed,
//   EventSendFailures, EventSendBlockedMs, TerminalBusForcedDeliveries
//
// Phase 7 (ai-chat/internal/prodhardening + ratelimit eviction):
//   LoadScenariosRun, LoadGenerationsOK, LoadGenerationsFail, LoadCancellations,
//   RateLimiterEvictions, PendingTokensRejected
// Policy snapshot: prodhardening.DefaultPolicy / MaxProviderStreamAttemptsPerGeneration
package perfmetrics

// TimingFields are the keys to grep from structured logs after a
// representative interactive edit. Fill Observed from a real run; leave
// zero when not yet measured.
type TimingFields struct {
	TenantID           string
	ChatID             string
	GenerationID       string
	Provider           string
	Model              string
	Status             string
	TotalMs            int64
	QueueWaitMs        int64
	PreModelMs         int64
	DraftLoadMs        int64
	WorkspaceSyncMs    int64
	URLFetchMs         int64
	ThemeContextMs     int64
	IntentContextMs    int64
	SnapshotBaseMs     int64
	HistoryMs          int64
	TTFTMs             int64
	TTFTAvailable      bool
	ModelElapsedMs     int64
	ToolElapsedMs      int64
	CheckAndRepairMs   int64
	DraftPersistMs     int64
	TotalGenerateCalls int
	InitialGenerateCalls int
	RepairGenerateCalls  int
	RepairAttempts       int
	ProviderRetries      int
	Iterations         int
	ModelCalls         int
	ToolCalls          int
	ListCalls          int
	ReadCalls          int
	GrepCalls          int
	ValidateCalls      int
	ProposeCalls       int
	FlowPOSReads       int
	FlowPOSListFiles   int64
	FlowPOSReadFile    int64
	FlowPOSWriteFile   int64
	ThemeCacheHits     int64
	ThemeCacheMisses   int64
	ThemeCacheEntries  int
	ThemeCacheBytes    int64
	ListFilesCacheHits int64
	ReadFileCacheHits  int64
	CacheHits          int64 // deprecated alias; prefer ThemeCacheHits
	CacheMisses        int64 // deprecated alias; prefer ThemeCacheMisses
	InputTokens        int64
	OutputTokens       int64
	ReasoningTokens    int64
	CacheReadTokens    int64
	CacheCreationTokens int64
	RetryCount         int
	ZeroToolNudges     int
	ExplorationTools   int
	StreamTimeouts     int
	StreamTruncations  int
	LocalSearchMs      int64
}

// ScenarioNames are the repeatable request classes for manual / eval runs.
var ScenarioNames = []string{
	"simple_css_edit",
	"button_style_edit",
	"product_page_edit",
	"multi_file_edit",
	"gallery_redesign",
	"grep_heavy",
	"themecheck_repair",
	"complex_page_redesign",
}

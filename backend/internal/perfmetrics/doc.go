// Package perfmetrics documents the structured fields emitted by
// ai.Generator and themebuild.Service for comparing provider latency.
// Live numbers come from process logs (ai: generate call finished,
// ai: model call timing, themebuild cache_hits / pre_model_ms /
// queue_wait_ms) — this package does not invent benchmarks.
package perfmetrics

// TimingFields are the keys to grep from structured logs after a
// representative interactive edit. Fill Observed from a real run; leave
// zero when not yet measured.
type TimingFields struct {
	Provider           string
	Model              string
	TotalMs            int64
	QueueWaitMs        int64
	PreModelMs         int64
	TTFTMs             int64
	ModelElapsedMs     int64
	ToolElapsedMs      int64
	Iterations         int
	ModelCalls         int
	ToolCalls          int
	GrepCalls          int
	FlowPOSReads       int
	CacheHits          int64
	CacheMisses        int64
	InputTokens        int64
	OutputTokens       int64
	ReasoningTokens    int64
	CacheReadTokens    int64
	RetryCount         int
	ZeroToolNudges     int
	ExplorationTools   int
	StreamTimeouts     int
	StreamTruncations  int
	WorkspaceSyncMs    int64
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

package ai

import (
	"sync"
	"time"
)

// TurnMetrics accumulates Phase 0 observability counters for one merchant
// generation (one doGenerate). It is generation-scoped — attached to
// ThemeContext and never stored in a global map. Concurrent tool/grep
// goroutines may call Record* methods; all mutations are synchronized.
type TurnMetrics struct {
	mu sync.Mutex

	GenerateCalls        int
	InitialGenerateCalls int
	RepairGenerateCalls  int
	ProviderRetries      int
	RepairAttempts       int

	ModelIterations int
	ModelElapsedMs  int64
	ToolElapsedMs   int64

	// FirstTTFTMs is wall time from the first successful model-call start to
	// first meaningful provider output in this turn. Unset when no stream
	// ever produced progress (see FirstTTFTAvailable).
	FirstTTFTMs        int64
	FirstTTFTAvailable bool

	ToolCalls     int
	ListCalls     int
	ReadCalls     int
	GrepCalls     int
	ValidateCalls int
	ProposeCalls  int

	InputTokens  int64
	OutputTokens int64

	ReasoningTokens   int64
	ReasoningReported bool

	CacheReadTokens       int64
	CacheReadReported     bool
	CacheCreationTokens   int64
	CacheCreationReported bool

	GrepFilesScanned     int
	GrepMatchesFound     int
	GrepFlowPOSReads     int
	GrepFlowPOSElapsedMs int64
	GrepCacheHits        int64
	GrepCacheMisses      int64
	GrepPeakConcurrent   int
	GrepMaxConcurrency   int

	// Phase 4 — repair / retry aggregates for one generation.
	RepairElapsedMs       int64
	ProviderRetryWaitMs   int64
	RepairBudgetExhausted bool
	RepairSkipped         bool
	RepairSkipReason      string
	RepairReasonCategory  string

	// Phase 5 — DeepSeek / provider request-shape observability.
	ZeroToolNudges     int
	ForcedProposeCount int
	ProposeNudgeCount  int
	SystemPromptBytes  int
	ToolSchemaBytes    int
	MessageInputBytes  int
	ToolResultBytes    int64
	LastMessageCount   int
}

// ComputeTTFTMs returns elapsed ms from modelCallStart to firstProgress.
// available is false when firstProgress never arrived (do not invent a value).
func ComputeTTFTMs(modelCallStart, firstProgress time.Time) (ms int64, available bool) {
	if modelCallStart.IsZero() || firstProgress.IsZero() {
		return 0, false
	}
	return firstProgress.Sub(modelCallStart).Milliseconds(), true
}


// RecordGenerateCall increments Generate call counters. repair distinguishes
// checkAndRepair Generate calls from the initial proposal loop.
func (m *TurnMetrics) RecordGenerateCall(repair bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.GenerateCalls++
	if repair {
		m.RepairGenerateCalls++
	} else {
		m.InitialGenerateCalls++
	}
}

// RecordProviderRetries adds stream-accumulate retries within one Generate.
func (m *TurnMetrics) RecordProviderRetries(n int) {
	if m == nil || n <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ProviderRetries += n
}

// AddProviderRetryWaitMs accumulates sleep time spent waiting between
// provider stream retries (never includes model inference time).
func (m *TurnMetrics) AddProviderRetryWaitMs(ms int64) {
	if m == nil || ms <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ProviderRetryWaitMs += ms
}

// AddRepairElapsedMs accumulates wall time of AI repair Generate calls.
func (m *TurnMetrics) AddRepairElapsedMs(ms int64) {
	if m == nil || ms <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RepairElapsedMs += ms
}

// SetRepairOutcome records terminal repair-loop classification for logs.
func (m *TurnMetrics) SetRepairOutcome(skipped bool, skipReason, reasonCategory string, budgetExhausted bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RepairSkipped = skipped
	m.RepairSkipReason = skipReason
	if reasonCategory != "" {
		m.RepairReasonCategory = reasonCategory
	}
	if budgetExhausted {
		m.RepairBudgetExhausted = true
	}
}

// RecordZeroToolNudge counts a toolless model turn that required a nudge.
func (m *TurnMetrics) RecordZeroToolNudge() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ZeroToolNudges++
}

// RecordForcedPropose counts a tool_choice=propose_changes force.
func (m *TurnMetrics) RecordForcedPropose() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ForcedProposeCount++
}

// RecordProposeNudge counts a DeepSeek thinking-on path that could not force
// named tool_choice and instead relied on a text nudge + tool_choice any.
func (m *TurnMetrics) RecordProposeNudge() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ProposeNudgeCount++
}

// RecordRequestShape stores byte-size / message-count observability for one
// Generate call (sizes only — never prompt text).
func (m *TurnMetrics) RecordRequestShape(systemBytes, toolSchemaBytes, messageBytes, messageCount int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if systemBytes > m.SystemPromptBytes {
		m.SystemPromptBytes = systemBytes
	}
	if toolSchemaBytes > m.ToolSchemaBytes {
		m.ToolSchemaBytes = toolSchemaBytes
	}
	if messageBytes > m.MessageInputBytes {
		m.MessageInputBytes = messageBytes
	}
	if messageCount > m.LastMessageCount {
		m.LastMessageCount = messageCount
	}
}

// AddToolResultBytes accumulates tool_result payload sizes (not contents).
func (m *TurnMetrics) AddToolResultBytes(n int) {
	if m == nil || n <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ToolResultBytes += int64(n)
}

// SetRepairAttempts records the highest themecheck repair attempt number.
func (m *TurnMetrics) SetRepairAttempts(n int) {
	if m == nil || n <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if n > m.RepairAttempts {
		m.RepairAttempts = n
	}
}

// RecordModelIteration folds one tool-loop iteration's timing/tokens.
// ttftAvailable false means the provider never emitted progress for that call.
func (m *TurnMetrics) RecordModelIteration(elapsedMs, ttftMs int64, ttftAvailable bool, input, output, reasoning int64, reasoningOK bool, cacheRead, cacheCreate int64, cacheReadOK, cacheCreateOK bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ModelIterations++
	m.ModelElapsedMs += elapsedMs
	if ttftAvailable && !m.FirstTTFTAvailable {
		m.FirstTTFTMs = ttftMs
		m.FirstTTFTAvailable = true
	}
	m.InputTokens += input
	m.OutputTokens += output
	if reasoningOK {
		m.ReasoningTokens += reasoning
		m.ReasoningReported = true
	}
	if cacheReadOK {
		m.CacheReadTokens += cacheRead
		m.CacheReadReported = true
	}
	if cacheCreateOK {
		m.CacheCreationTokens += cacheCreate
		m.CacheCreationReported = true
	}
}

// RecordToolCall increments tool counters and elapsed time.
func (m *TurnMetrics) RecordToolCall(name string, elapsedMs int64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ToolCalls++
	m.ToolElapsedMs += elapsedMs
	switch name {
	case toolNameListThemeFiles:
		m.ListCalls++
	case toolNameReadThemeFile:
		m.ReadCalls++
	case toolNameGrepTheme:
		m.GrepCalls++
	case toolNameValidateChanges:
		m.ValidateCalls++
	case toolNameProposeChanges:
		m.ProposeCalls++
	}
}

// RecordProposeCall counts a propose_changes that Generate handles itself
// (not via ToolExecutor).
func (m *TurnMetrics) RecordProposeCall() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ProposeCalls++
	m.ToolCalls++
}

// GrepIOMetrics is one grep_theme call's I/O / concurrency snapshot for
// Phase 0 + Phase 3 observability.
type GrepIOMetrics struct {
	FilesScanned      int
	Matches           int
	FlowPOSReads      int
	FlowPOSElapsedMs  int64
	CacheHits         int64
	CacheMisses       int64
	PeakConcurrent    int
	MaxConcurrency    int
}

// RecordGrep folds one grep_theme execution's I/O counters.
func (m *TurnMetrics) RecordGrep(g GrepIOMetrics) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.GrepFilesScanned += g.FilesScanned
	m.GrepMatchesFound += g.Matches
	m.GrepFlowPOSReads += g.FlowPOSReads
	m.GrepFlowPOSElapsedMs += g.FlowPOSElapsedMs
	m.GrepCacheHits += g.CacheHits
	m.GrepCacheMisses += g.CacheMisses
	if g.PeakConcurrent > m.GrepPeakConcurrent {
		m.GrepPeakConcurrent = g.PeakConcurrent
	}
	if g.MaxConcurrency > m.GrepMaxConcurrency {
		m.GrepMaxConcurrency = g.MaxConcurrency
	}
}

// Snapshot returns a mutex-free copy safe to read without holding m.mu.
func (m *TurnMetrics) Snapshot() TurnMetricsSnapshot {
	if m == nil {
		return TurnMetricsSnapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return TurnMetricsSnapshot{
		GenerateCalls:            m.GenerateCalls,
		InitialGenerateCalls:     m.InitialGenerateCalls,
		RepairGenerateCalls:      m.RepairGenerateCalls,
		ProviderRetries:          m.ProviderRetries,
		RepairAttempts:           m.RepairAttempts,
		ModelIterations:          m.ModelIterations,
		ModelElapsedMs:           m.ModelElapsedMs,
		ToolElapsedMs:            m.ToolElapsedMs,
		FirstTTFTMs:              m.FirstTTFTMs,
		FirstTTFTAvailable:       m.FirstTTFTAvailable,
		ToolCalls:                m.ToolCalls,
		ListCalls:                m.ListCalls,
		ReadCalls:                m.ReadCalls,
		GrepCalls:                m.GrepCalls,
		ValidateCalls:            m.ValidateCalls,
		ProposeCalls:             m.ProposeCalls,
		InputTokens:              m.InputTokens,
		OutputTokens:             m.OutputTokens,
		ReasoningTokens:          m.ReasoningTokens,
		ReasoningReported:        m.ReasoningReported,
		CacheReadTokens:          m.CacheReadTokens,
		CacheReadReported:        m.CacheReadReported,
		CacheCreationTokens:      m.CacheCreationTokens,
		CacheCreationReported:    m.CacheCreationReported,
		GrepFilesScanned:         m.GrepFilesScanned,
		GrepMatchesFound:         m.GrepMatchesFound,
		GrepFlowPOSReads:         m.GrepFlowPOSReads,
		GrepFlowPOSElapsedMs:     m.GrepFlowPOSElapsedMs,
		GrepCacheHits:            m.GrepCacheHits,
		GrepCacheMisses:          m.GrepCacheMisses,
		GrepPeakConcurrent:       m.GrepPeakConcurrent,
		GrepMaxConcurrency:       m.GrepMaxConcurrency,
		RepairElapsedMs:          m.RepairElapsedMs,
		ProviderRetryWaitMs:      m.ProviderRetryWaitMs,
		RepairBudgetExhausted:    m.RepairBudgetExhausted,
		RepairSkipped:            m.RepairSkipped,
		RepairSkipReason:         m.RepairSkipReason,
		RepairReasonCategory:     m.RepairReasonCategory,
		ZeroToolNudges:           m.ZeroToolNudges,
		ForcedProposeCount:       m.ForcedProposeCount,
		ProposeNudgeCount:        m.ProposeNudgeCount,
		SystemPromptBytes:        m.SystemPromptBytes,
		ToolSchemaBytes:          m.ToolSchemaBytes,
		MessageInputBytes:        m.MessageInputBytes,
		ToolResultBytes:          m.ToolResultBytes,
		LastMessageCount:         m.LastMessageCount,
	}
}

// TurnMetricsSnapshot is a plain copy of TurnMetrics without the mutex.
type TurnMetricsSnapshot struct {
	GenerateCalls        int
	InitialGenerateCalls int
	RepairGenerateCalls  int
	ProviderRetries      int
	RepairAttempts       int

	ModelIterations int
	ModelElapsedMs  int64
	ToolElapsedMs   int64

	FirstTTFTMs        int64
	FirstTTFTAvailable bool

	ToolCalls     int
	ListCalls     int
	ReadCalls     int
	GrepCalls     int
	ValidateCalls int
	ProposeCalls  int

	InputTokens  int64
	OutputTokens int64

	ReasoningTokens   int64
	ReasoningReported bool

	CacheReadTokens       int64
	CacheReadReported     bool
	CacheCreationTokens   int64
	CacheCreationReported bool

	GrepFilesScanned     int
	GrepMatchesFound     int
	GrepFlowPOSReads     int
	GrepFlowPOSElapsedMs int64
	GrepCacheHits        int64
	GrepCacheMisses      int64
	GrepPeakConcurrent   int
	GrepMaxConcurrency   int

	RepairElapsedMs       int64
	ProviderRetryWaitMs   int64
	RepairBudgetExhausted bool
	RepairSkipped         bool
	RepairSkipReason      string
	RepairReasonCategory  string

	ZeroToolNudges     int
	ForcedProposeCount int
	ProposeNudgeCount  int
	SystemPromptBytes  int
	ToolSchemaBytes    int
	MessageInputBytes  int
	ToolResultBytes    int64
	LastMessageCount   int
}

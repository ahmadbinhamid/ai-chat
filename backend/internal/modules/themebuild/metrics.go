package themebuild

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/safego"
)

// GenerationMetrics is one finished generation's timing and usage, stored in generation_metrics.
type GenerationMetrics struct {
	GenerationID string
	ChatID       string
	TenantID     uint64
	ModelID      string
	Effort       string
	Outcome      string

	QueueWait    time.Duration
	ContextBuild time.Duration
	FirstToken   time.Duration
	Model        time.Duration
	Tool         time.Duration
	Validation   time.Duration
	Repair       time.Duration
	Total        time.Duration

	ToolCalls      int
	Iterations     int
	RepairAttempts int
	PreloadedFiles int
	PreloadedBytes int

	InputTokens     int64
	OutputTokens    int64
	CacheReadTokens int64
	CostUSD         *float64
}

// generationOutcome maps how doGenerate ended to the status this worker records; ours is false when the row was
// finished, reaped or re-queued by someone else, so neither the end nor its metrics are this worker's to write.
func generationOutcome(err error, cancelledByUser bool) (outcome string, ours bool) {
	switch {
	case errors.Is(err, ErrGenerationNotRunning):
		return "", false
	case err == nil:
		return GenerationStatusSucceeded, true
	case cancelledByUser:
		return GenerationStatusCancelled, true
	default:
		return GenerationStatusFailed, true
	}
}

// turnMetrics accumulates one generation's timings; only its own goroutine touches it, and nil is a no-op.
type turnMetrics struct {
	contextStart  time.Time
	contextBuild  time.Duration
	generateCalls int

	firstToken, model, tool time.Duration
	toolCalls, iterations   int
	validation, repair      time.Duration
	repairAttempts          int
	preloadedFiles          int
	preloadedBytes          int

	inputTokens, outputTokens, cacheReadTokens int64
	cost                                       *float64
	modelID, effort                            string
}

func (m *turnMetrics) startContext() {
	if m != nil {
		m.contextStart = time.Now()
	}
}

// contextBuilt marks the first model call; later calls are retries or repairs, not context building.
func (m *turnMetrics) contextBuilt() {
	if m != nil && m.generateCalls == 0 && !m.contextStart.IsZero() {
		m.contextBuild = time.Since(m.contextStart)
	}
}

// addGenerate folds one Generate call's result in; r is nil when the call failed.
func (m *turnMetrics) addGenerate(r *ai.Result) {
	if m == nil {
		return
	}
	m.generateCalls++
	if r == nil {
		return
	}
	if m.generateCalls == 1 {
		m.firstToken = r.Timings.FirstToken
	}
	m.model += r.Timings.Model
	m.tool += r.Timings.Tool
	m.iterations += r.Timings.Iterations
	m.toolCalls += r.ExplorationToolCalls
	m.inputTokens += r.InputTokens
	m.outputTokens += r.OutputTokens
	m.cacheReadTokens += r.Timings.CacheReadTokens
	m.cost = addCost(m.cost, r.CostUSD)
	if r.ModelID != "" {
		m.modelID, m.effort = r.ModelID, r.Effort
	}
}

func (m *turnMetrics) setPreload(files, bytes int) {
	if m != nil {
		m.preloadedFiles, m.preloadedBytes = files, bytes
	}
}

func (m *turnMetrics) addValidation(d time.Duration) {
	if m != nil {
		m.validation += d
	}
}

func (m *turnMetrics) addRepair(d time.Duration) {
	if m != nil {
		m.repair += d
		m.repairAttempts++
	}
}

// metricsRow builds g's generation_metrics row; m is nil for a generation that never reached the model.
func metricsRow(m *turnMetrics, c chat.Chat, g Generation, outcome string, total time.Duration) GenerationMetrics {
	row := GenerationMetrics{
		GenerationID: g.ID, ChatID: c.ID, TenantID: g.TenantID, ModelID: g.ModelID, Effort: g.Effort,
		Outcome: outcome, Total: total,
	}
	if g.QueuedAt != nil && g.StartedAt != nil && g.StartedAt.After(*g.QueuedAt) {
		row.QueueWait = g.StartedAt.Sub(*g.QueuedAt)
	}
	if m == nil {
		return row
	}
	if m.modelID != "" {
		row.ModelID, row.Effort = m.modelID, m.effort
	}
	row.ContextBuild, row.FirstToken, row.Model, row.Tool = m.contextBuild, m.firstToken, m.model, m.tool
	row.Validation, row.Repair, row.RepairAttempts = m.validation, m.repair, m.repairAttempts
	row.ToolCalls, row.Iterations = m.toolCalls, m.iterations
	row.PreloadedFiles, row.PreloadedBytes = m.preloadedFiles, m.preloadedBytes
	row.InputTokens, row.OutputTokens, row.CacheReadTokens, row.CostUSD = m.inputTokens, m.outputTokens, m.cacheReadTokens, m.cost
	return row
}

// recordGenerationMetrics writes the row in the background: a slow or failing metrics write must never fail the
// generation or hold up the chat's next queued one.
func (s *Service) recordGenerationMetrics(row GenerationMetrics) {
	go func() {
		defer safego.Recover("themebuild.recordGenerationMetrics")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.repo.InsertGenerationMetrics(ctx, row); err != nil {
			slog.Warn("failed to record generation metrics", "generation_id", row.GenerationID, "error", err)
		}
	}()
}

package themebuild

import (
	"context"
	"database/sql"
	"time"
)

func (r *Repository) InsertGenerationMetrics(ctx context.Context, m GenerationMetrics) error {
	now := time.Now().UTC()
	modelID := sql.NullString{String: m.ModelID, Valid: m.ModelID != ""}
	effort := sql.NullString{String: m.Effort, Valid: m.Effort != ""}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO generation_metrics
			(generation_id, chat_id, tenant_id, model_id, effort, escalated, redesign, outcome,
			 queue_wait_ms, context_build_ms, first_token_ms, model_ms, tool_ms, tool_calls, preloaded_files, preloaded_bytes, iterations,
			 validation_ms, repair_attempts, repair_ms, input_tokens, output_tokens, cache_read_tokens, cost_usd,
			 total_ms, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, m.GenerationID, m.ChatID, m.TenantID, modelID, effort, m.Escalated, m.Redesign, m.Outcome,
		m.QueueWait.Milliseconds(), m.ContextBuild.Milliseconds(), m.FirstToken.Milliseconds(),
		m.Model.Milliseconds(), m.Tool.Milliseconds(), m.ToolCalls, m.PreloadedFiles, m.PreloadedBytes, m.Iterations,
		m.Validation.Milliseconds(), m.RepairAttempts, m.Repair.Milliseconds(),
		m.InputTokens, m.OutputTokens, m.CacheReadTokens, m.CostUSD,
		m.Total.Milliseconds(), now, now)
	return err
}

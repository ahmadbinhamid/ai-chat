package themebuild

import (
	"context"
	"time"
)

// UpsertClientTiming keeps earlier values the new report leaves out, so a retry or a later partial report never erases
// what an earlier one recorded.
func (r *Repository) UpsertClientTiming(ctx context.Context, generationID, chatID string, tenantID uint64, t ClientTiming) error {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO generation_client_timing
			(generation_id, chat_id, tenant_id, post_ms, ws_connect_ms, first_event_ms, first_progress_ms, completed_ms,
			 preview_visible_ms, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) AS new
		ON DUPLICATE KEY UPDATE
			post_ms            = COALESCE(new.post_ms, generation_client_timing.post_ms),
			ws_connect_ms      = COALESCE(new.ws_connect_ms, generation_client_timing.ws_connect_ms),
			first_event_ms     = COALESCE(new.first_event_ms, generation_client_timing.first_event_ms),
			first_progress_ms  = COALESCE(new.first_progress_ms, generation_client_timing.first_progress_ms),
			completed_ms       = COALESCE(new.completed_ms, generation_client_timing.completed_ms),
			preview_visible_ms = COALESCE(new.preview_visible_ms, generation_client_timing.preview_visible_ms),
			updated_at         = new.updated_at
	`, generationID, chatID, tenantID, t.PostMs, t.WSConnectMs, t.FirstEventMs, t.FirstProgressMs, t.CompletedMs,
		t.PreviewVisibleMs, now, now)
	return err
}

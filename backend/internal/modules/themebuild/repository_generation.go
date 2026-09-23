package themebuild

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"ai-chat/internal/ai"

	"github.com/go-sql-driver/mysql"
)

// MySQL ER_DUP_ENTRY code; detects already-running via uniq_generations_running_chat index.
const mysqlDuplicateKeyErrNumber = 1062

// Shared column list for all Scan calls; prevents drift between queries.
const generationColumns = `
	id, chat_id, tenant_id, status, error, attempts,
	prompt, reference_url, user_message_id, theme_slug, mode, queued_at, started_at, finished_at
`

// Tests only; enforces "one running per chat" atomically via uniq_generations_running_chat.
func (r *Repository) StartGeneration(ctx context.Context, id, chatID string, tenantID uint64) error {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO generations (id, chat_id, tenant_id, status, attempts, prompt, started_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?)
	`, id, chatID, tenantID, GenerationStatusRunning, "", now, now, now)
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == mysqlDuplicateKeyErrNumber {
			return ErrGenerationInProgress
		}
		return err
	}
	return nil
}

// Caps queue depth; independent of ratelimit, which bounds rate not depth.
const maxQueueDepth = 10

var ErrQueueFull = errors.New("this chat already has the maximum number of pending generations queued")

// Runs in transaction with SELECT FOR UPDATE to prevent racing enqueues blowing past cap.
func (r *Repository) EnqueueGeneration(ctx context.Context, g Generation) (position int, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var pending int
	err = tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM generations
		WHERE chat_id = ? AND status IN (?, ?)
		FOR UPDATE
	`, g.ChatID, GenerationStatusRunning, GenerationStatusQueued).Scan(&pending)
	if err != nil {
		return 0, err
	}
	if pending >= maxQueueDepth {
		return 0, ErrQueueFull
	}

	enqueuedAt := time.Now().UTC()
	referenceURL := sql.NullString{String: g.ReferenceURL, Valid: g.ReferenceURL != ""}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO generations
			(id, chat_id, tenant_id, status, attempts, prompt, reference_url, user_message_id, theme_slug, mode, queued_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?)
	`, g.ID, g.ChatID, g.TenantID, GenerationStatusQueued, g.Prompt, referenceURL, g.UserMessageID, g.ThemeSlug, g.Mode, enqueuedAt, enqueuedAt, enqueuedAt)
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return pending, nil
}

// Promotes oldest queued to running; ties on queued_at break on id.
func (r *Repository) DequeueNext(ctx context.Context, chatID string) (Generation, error) {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE generations
		SET status = ?, started_at = ?, updated_at = ?
		WHERE chat_id = ? AND status = ?
		ORDER BY queued_at, id
		LIMIT 1
	`, GenerationStatusRunning, now, now, chatID, GenerationStatusQueued)
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == mysqlDuplicateKeyErrNumber {
			return Generation{}, ErrGenerationInProgress
		}
		return Generation{}, err
	}

	n, err := res.RowsAffected()
	if err != nil {
		return Generation{}, err
	}
	if n == 0 {
		return Generation{}, ErrNotFound
	}

	// MySQL no UPDATE RETURNING; follow-up read safe (one running row guaranteed).
	row := r.db.QueryRowContext(ctx, `
		SELECT `+generationColumns+`
		FROM generations WHERE chat_id = ? AND status = ? LIMIT 1
	`, chatID, GenerationStatusRunning)
	return scanGeneration(row)
}

// Cancels queued rows only; returns ErrNotFound for running/finished/nonexistent.
func (r *Repository) CancelQueued(ctx context.Context, chatID, generationID string) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE generations SET status = ?, finished_at = ?, updated_at = ?
		WHERE id = ? AND chat_id = ? AND status = ?
	`, GenerationStatusCancelled, now, now, generationID, chatID, GenerationStatusQueued)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Returns running + queued rows oldest first; running naturally first due to DequeueNext.
func (r *Repository) ListPending(ctx context.Context, chatID string) ([]Generation, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+generationColumns+`
		FROM generations
		WHERE chat_id = ? AND status IN (?, ?)
		ORDER BY queued_at, id
	`, chatID, GenerationStatusRunning, GenerationStatusQueued)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var gens []Generation
	for rows.Next() {
		g, err := scanGeneration(rows)
		if err != nil {
			return nil, err
		}
		gens = append(gens, g)
	}
	return gens, rows.Err()
}

// Returns chats with queued rows but no running row; used by reaper for dead-pod recovery.
func (r *Repository) ChatsWithOrphanedQueues(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT q.chat_id
		FROM generations q
		WHERE q.status = ?
		AND NOT EXISTS (
			SELECT 1 FROM generations r WHERE r.chat_id = q.chat_id AND r.status = ?
		)
	`, GenerationStatusQueued, GenerationStatusRunning)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var chatIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		chatIDs = append(chatIDs, id)
	}
	return chatIDs, rows.Err()
}

// Marks running generation finished/failed; no-op if already reaped.
func (r *Repository) EndGeneration(ctx context.Context, chatID string, genErr error) error {
	status := GenerationStatusSucceeded
	var errMsg *string
	if genErr != nil {
		status = GenerationStatusFailed
		// Sanitize: never leak AI provider name/URL/request ID; column feeds merchant-visible error.
		msg := ai.SanitizeError(genErr)
		if errors.Is(genErr, errSessionExpired) {
			msg = errSessionExpired.Error()
		}
		errMsg = &msg
	}
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		UPDATE generations SET status = ?, error = ?, finished_at = ?, updated_at = ?
		WHERE chat_id = ? AND status = ?
	`, status, errMsg, now, now, chatID, GenerationStatusRunning)
	return err
}

// Records themecheck retry attempts; called from checkAndRepair.
func (r *Repository) SetGenerationAttempts(ctx context.Context, chatID string, attempts int) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE generations SET attempts = ?, updated_at = ? WHERE chat_id = ? AND status = ?
	`, attempts, time.Now().UTC(), chatID, GenerationStatusRunning)
	return err
}

// Returns most recently started generation; NULL sorts smallest (queued last).
func (r *Repository) GetGeneration(ctx context.Context, chatID string) (Generation, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+generationColumns+`
		FROM generations WHERE chat_id = ? ORDER BY started_at DESC, queued_at DESC LIMIT 1
	`, chatID)
	g, err := scanGeneration(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Generation{}, ErrNotFound
	}
	return g, err
}

// Returns row scoped to chatID; used by CancelQueuedGeneration.
func (r *Repository) GetGenerationByID(ctx context.Context, chatID, generationID string) (Generation, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+generationColumns+`
		FROM generations WHERE id = ? AND chat_id = ?
	`, generationID, chatID)
	g, err := scanGeneration(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Generation{}, ErrNotFound
	}
	return g, err
}

// Marks running generation cancelled; counterpart to CancelQueued.
func (r *Repository) EndGenerationCancelled(ctx context.Context, chatID string) error {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		UPDATE generations SET status = ?, finished_at = ?, updated_at = ?
		WHERE chat_id = ? AND status = ?
	`, GenerationStatusCancelled, now, now, chatID, GenerationStatusRunning)
	return err
}

// Durable counterpart to live EventTypeCancelRequested; re-stamps for idempotent retries.
func (r *Repository) RequestGenerationCancellation(ctx context.Context, chatID, generationID string) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE generations SET cancel_requested_at = ?, updated_at = ?
		WHERE id = ? AND chat_id = ? AND status = ?
	`, now, now, generationID, chatID, GenerationStatusRunning)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Reports pending cancel request; returns false if row gone (caller records outcome).
func (r *Repository) IsCancellationRequested(ctx context.Context, chatID, generationID string) (bool, error) {
	var requested sql.NullTime
	err := r.db.QueryRowContext(ctx, `
		SELECT cancel_requested_at FROM generations WHERE id = ? AND chat_id = ?
	`, generationID, chatID).Scan(&requested)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return requested.Valid, nil
}

// Best-effort; failure must never fail the generation itself (caller logs only).
func (r *Repository) UpdateGenerationHeartbeat(ctx context.Context, id string) error {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		UPDATE generations SET last_heartbeat_at = ?, updated_at = ? WHERE id = ?
	`, now, now, id)
	return err
}

// Fails running rows older than heartbeatTimeout; NULL rows fall back to startedFallback.
func (r *Repository) ReapStaleGenerations(ctx context.Context, heartbeatTimeout, startedFallback time.Duration) (int64, error) {
	now := time.Now().UTC()
	heartbeatCutoff := now.Add(-heartbeatTimeout)
	startedCutoff := now.Add(-startedFallback)
	res, err := r.db.ExecContext(ctx, `
		UPDATE generations
		SET status = ?, error = ?, finished_at = ?, updated_at = ?
		WHERE status = ?
		  AND (
		    (last_heartbeat_at IS NOT NULL AND last_heartbeat_at < ?)
		    OR (last_heartbeat_at IS NULL AND started_at < ?)
		  )
	`, GenerationStatusFailed, "generation timed out (reaped)", now, now, GenerationStatusRunning, heartbeatCutoff, startedCutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

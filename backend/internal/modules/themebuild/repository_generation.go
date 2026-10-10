package themebuild

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"ai-chat/internal/ai"

	"github.com/go-sql-driver/mysql"
)

// MySQL ER_DUP_ENTRY code; detects already-running via uniq_generations_running_chat index.
const mysqlDuplicateKeyErrNumber = 1062

// MySQL ER_LOCK_DEADLOCK: an enqueue's locking read and a concurrent dequeue on the same chat can deadlock; MySQL rolls
// one back whole, so it is safe to run again.
const mysqlDeadlockErrNumber = 1213

const deadlockRetries = 3

// retryOnDeadlock runs fn again, after a short random pause, while it fails with a deadlock.
func retryOnDeadlock(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 1; ; attempt++ {
		err = fn()
		var mysqlErr *mysql.MySQLError
		if !errors.As(err, &mysqlErr) || mysqlErr.Number != mysqlDeadlockErrNumber || attempt == deadlockRetries {
			return err
		}
		slog.Warn("generation queue deadlock; retrying", "attempt", attempt)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(5+rand.IntN(20)) * time.Millisecond):
		}
	}
}

// Shared column list for all Scan calls; prevents drift between queries.
const generationColumns = `
	id, chat_id, tenant_id, status, error, attempts,
	prompt, reference_url, user_message_id, theme_slug, mode, model_id, effort, thinking_off, auto_selected, redesign, preview_route, focus_file,
	resume_count, awaiting_resume_since,
	queued_at, started_at, finished_at
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
	err = retryOnDeadlock(ctx, func() error {
		position, err = r.enqueueGenerationOnce(ctx, g)
		return err
	})
	return position, err
}

func (r *Repository) enqueueGenerationOnce(ctx context.Context, g Generation) (position int, err error) {
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
	modelID := sql.NullString{String: g.ModelID, Valid: g.ModelID != ""}
	effort := sql.NullString{String: g.Effort, Valid: g.Effort != ""}
	focusFile := sql.NullString{String: g.FocusFile, Valid: g.FocusFile != ""}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO generations
			(id, chat_id, tenant_id, status, attempts, prompt, reference_url, user_message_id, theme_slug, mode, model_id, effort, thinking_off,
			 auto_selected, redesign, preview_route, focus_file, queued_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, g.ID, g.ChatID, g.TenantID, GenerationStatusQueued, g.Prompt, referenceURL, g.UserMessageID, g.ThemeSlug, g.Mode, modelID, effort, g.ThinkingOff,
		g.AutoSelected, g.Redesign, g.PreviewRoute, focusFile, enqueuedAt, enqueuedAt, enqueuedAt)
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return pending, nil
}

// Promotes oldest queued to running; ties on queued_at break on id.
func (r *Repository) DequeueNext(ctx context.Context, chatID string) (g Generation, err error) {
	err = retryOnDeadlock(ctx, func() error {
		g, err = r.dequeueNextOnce(ctx, chatID)
		return err
	})
	return g, err
}

func (r *Repository) dequeueNextOnce(ctx context.Context, chatID string) (Generation, error) {
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

// Returns chats with queued rows but no running row; used by reaper for dead-pod recovery. A chat whose queued rows
// were heartbeated since waitingSince has a live process waiting for capacity, so it is not orphaned.
func (r *Repository) ChatsWithOrphanedQueues(ctx context.Context, waitingSince time.Time) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT q.chat_id
		FROM generations q
		WHERE q.status = ?
		AND NOT EXISTS (
			SELECT 1 FROM generations r WHERE r.chat_id = q.chat_id AND r.status = ?
		)
		AND NOT EXISTS (
			SELECT 1 FROM generations w WHERE w.chat_id = q.chat_id AND w.status = ? AND w.last_heartbeat_at >= ?
		)
	`, GenerationStatusQueued, GenerationStatusRunning, GenerationStatusQueued, waitingSince)
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

// Marks running generation finished/failed; ErrGenerationNotRunning if it was already reaped, cancelled or finished.
func (r *Repository) EndGeneration(ctx context.Context, chatID, generationID string, genErr error) error {
	status := GenerationStatusSucceeded
	var errMsg *string
	if genErr != nil {
		status = GenerationStatusFailed
		// Sanitize: never leak AI provider name/URL/request ID; column feeds merchant-visible error.
		msg := ai.SanitizeError(genErr)
		// Already merchant-facing, and more specific than any sanitized category.
		if errors.Is(genErr, errSessionExpired) || errors.Is(genErr, errResumeExpired) || errors.Is(genErr, errInterruptedTwice) {
			msg = genErr.Error()
		}
		errMsg = &msg
	}
	now := time.Now().UTC()
	// Fenced by id: a reaped worker's late end must never land on the chat's next running generation.
	res, err := r.db.ExecContext(ctx, `
		UPDATE generations SET status = ?, error = ?, finished_at = ?, updated_at = ?
		WHERE id = ? AND chat_id = ? AND status = ?
	`, status, errMsg, now, now, generationID, chatID, GenerationStatusRunning)
	return requireRunningRowAffected(res, err)
}

func requireRunningRowAffected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrGenerationNotRunning
	}
	return nil
}

// RequeueInterrupted puts a generation cut off by a shutdown drain back in the queue, once: false when it was already
// re-queued before (the caller fails it) or is no longer running.
func (r *Repository) RequeueInterrupted(ctx context.Context, generationID string) (bool, error) {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE generations
		SET status = ?, started_at = NULL, last_heartbeat_at = NULL, resume_count = resume_count + 1, updated_at = ?
		WHERE id = ? AND status = ? AND resume_count = 0
	`, GenerationStatusQueued, now, generationID, GenerationStatusRunning)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// MarkQueuedAwaitingResume flags every queued generation as waiting for its sender: a restarting process holds none
// of their tokens. Returns how many are waiting, including ones flagged by an earlier start.
func (r *Repository) MarkQueuedAwaitingResume(ctx context.Context, at time.Time) (int64, error) {
	if _, err := r.db.ExecContext(ctx, `
		UPDATE generations SET awaiting_resume_since = ?, updated_at = ?
		WHERE status = ? AND awaiting_resume_since IS NULL
	`, at, at, GenerationStatusQueued); err != nil {
		return 0, err
	}
	var n int64
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM generations WHERE status = ? AND awaiting_resume_since IS NOT NULL
	`, GenerationStatusQueued).Scan(&n)
	return n, err
}

// CountAwaitingResume counts queued generations still waiting for their sender.
func (r *Repository) CountAwaitingResume(ctx context.Context) (int64, error) {
	var n int64
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM generations WHERE status = ? AND awaiting_resume_since IS NOT NULL
	`, GenerationStatusQueued).Scan(&n)
	return n, err
}

// AwaitingResume is a queued generation waiting for its sender, with the chat it belongs to.
type AwaitingResume struct {
	GenerationID string
	ChatID       string
}

// AwaitingResumeForUser lists a tenant's waiting generations sent by userID; only the sender's token may run them.
func (r *Repository) AwaitingResumeForUser(ctx context.Context, tenantID, userID uint64) ([]AwaitingResume, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT g.id, g.chat_id
		FROM generations g
		JOIN chat_messages m ON m.id = g.user_message_id
		WHERE g.tenant_id = ? AND g.status = ? AND g.awaiting_resume_since IS NOT NULL AND m.user_id = ?
		ORDER BY g.queued_at, g.id
	`, tenantID, GenerationStatusQueued, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []AwaitingResume
	for rows.Next() {
		var a AwaitingResume
		if err := rows.Scan(&a.GenerationID, &a.ChatID); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ReleaseClaim returns a just-dequeued generation to the queue, keeping its place (queued_at is unchanged).
func (r *Repository) ReleaseClaim(ctx context.Context, generationID string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE generations SET status = ?, started_at = NULL, updated_at = ?
		WHERE id = ? AND status = ?
	`, GenerationStatusQueued, time.Now().UTC(), generationID, GenerationStatusRunning)
	return err
}

// ErrGenerationNotRunning: the generation stopped being this process's to finish (cancelled, reaped or re-queued by
// a drain) before its turn committed, so the turn must be discarded.
var ErrGenerationNotRunning = errors.New("generation is no longer running")

// FinishGenerationTx marks a still-running generation succeeded inside the caller's transaction.
func (r *Repository) FinishGenerationTx(ctx context.Context, tx *sql.Tx, generationID string) error {
	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `
		UPDATE generations SET status = ?, error = NULL, finished_at = ?, updated_at = ?
		WHERE id = ? AND status = ?
	`, GenerationStatusSucceeded, now, now, generationID, GenerationStatusRunning)
	return requireRunningRowAffected(res, err)
}

// LockRunningGenerationTx row-locks a still-running generation so the caller's writes commit only while it is ours;
// a concurrent reap waits for the commit.
func (r *Repository) LockRunningGenerationTx(ctx context.Context, tx *sql.Tx, generationID string) error {
	var id string
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM generations WHERE id = ? AND status = ? FOR UPDATE
	`, generationID, GenerationStatusRunning).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrGenerationNotRunning
	}
	return err
}

// Records themecheck retry attempts; called from checkAndRepair.
func (r *Repository) SetGenerationAttempts(ctx context.Context, chatID, generationID string, attempts int) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE generations SET attempts = ?, updated_at = ? WHERE id = ? AND chat_id = ? AND status = ?
	`, attempts, time.Now().UTC(), generationID, chatID, GenerationStatusRunning)
	return requireRunningRowAffected(res, err)
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
func (r *Repository) EndGenerationCancelled(ctx context.Context, chatID, generationID string) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE generations SET status = ?, finished_at = ?, updated_at = ?
		WHERE id = ? AND chat_id = ? AND status = ?
	`, GenerationStatusCancelled, now, now, generationID, chatID, GenerationStatusRunning)
	return requireRunningRowAffected(res, err)
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

// TouchQueuedHeartbeat stamps every queued row of chatID while a process waits for capacity to run them; 0 means
// nothing is left queued.
func (r *Repository) TouchQueuedHeartbeat(ctx context.Context, chatID string) (int64, error) {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE generations SET last_heartbeat_at = ?, updated_at = ? WHERE chat_id = ? AND status = ?
	`, now, now, chatID, GenerationStatusQueued)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Best-effort; failure must never fail the generation itself (caller logs only).
func (r *Repository) UpdateGenerationHeartbeat(ctx context.Context, id string) error {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		UPDATE generations SET last_heartbeat_at = ?, updated_at = ? WHERE id = ? AND status = ?
	`, now, now, id, GenerationStatusRunning)
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

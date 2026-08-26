package themebuild

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"ai-chat/internal/ai"

	"github.com/go-sql-driver/mysql"
)

// mysqlDuplicateKeyErrNumber is MySQL's error code for a unique-constraint
// violation (ER_DUP_ENTRY) — used to tell "a generation is already running
// for this chat" (the uniq_generations_running_chat index rejecting a
// second insert/update) apart from any other insert/update failure.
const mysqlDuplicateKeyErrNumber = 1062

// generationColumns is the column list scanGeneration expects, in order —
// shared by every SELECT in this file so a column can't be added to one
// query and silently missed by scanGeneration's positional Scan in another.
const generationColumns = `
	id, chat_id, tenant_id, status, error, attempts,
	prompt, user_message_id, theme_slug, mode, queued_at, started_at, finished_at
`

// StartGeneration inserts a new running generation row for chatID directly
// — bypassing the queue entirely. Kept for the tests that seed a "something
// is already running" state directly (see e.g. generation_test.go) and as
// the one place a running row can be created with no prior queued row.
// Everyday traffic no longer calls this (see Service.Generate, which always
// enqueues first) — ErrGenerationInProgress is returned (not a raw DB
// error) if one is already running, the same as before this became one of
// two ways into "running": the uniq_generations_running_chat virtual-column
// index is what actually enforces this atomically, closing the race an
// in-memory map + mutex could only close within one process.
func (r *Repository) StartGeneration(ctx context.Context, id, chatID string, tenantID uint64) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO generations (id, chat_id, tenant_id, status, attempts, prompt, started_at)
		VALUES (?, ?, ?, ?, 0, ?, ?)
	`, id, chatID, tenantID, GenerationStatusRunning, "", time.Now().UTC())
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == mysqlDuplicateKeyErrNumber {
			return ErrGenerationInProgress
		}
		return err
	}
	return nil
}

// maxQueueDepth caps how many pending (running + queued) generations one
// chat may have at once. Independent of internal/ratelimit, which bounds
// how *fast* new generations can be enqueued — this bounds how many can be
// waiting at once, so a merchant holding Enter can't queue an unbounded
// number of Opus calls even while comfortably inside the rate limit.
const maxQueueDepth = 10

// ErrQueueFull means chatID already has maxQueueDepth pending generations —
// the caller should reject the new prompt (429) rather than let the queue
// grow without bound.
var ErrQueueFull = errors.New("this chat already has the maximum number of pending generations queued")

// EnqueueGeneration inserts a queued row for g.ChatID (g.ID must already be
// set by the caller — see Service.Generate) and returns how many
// generations (running + queued) were already ahead of it, so the caller
// can tell the merchant their position. Runs inside a transaction that
// locks chatID's existing pending rows for its duration (SELECT ... FOR
// UPDATE): without that, two enqueues racing right at maxQueueDepth could
// both read "9 pending" and both insert, blowing past the cap — the same
// class of race uniq_generations_running_chat exists to close for the
// running-row case, just enforced here at the application level since
// "at most 10" isn't expressible as a unique index.
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

	_, err = tx.ExecContext(ctx, `
		INSERT INTO generations
			(id, chat_id, tenant_id, status, attempts, prompt, user_message_id, theme_slug, mode, queued_at)
		VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?, ?)
	`, g.ID, g.ChatID, g.TenantID, GenerationStatusQueued, g.Prompt, g.UserMessageID, g.ThemeSlug, g.Mode, time.Now().UTC())
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return pending, nil
}

// DequeueNext atomically promotes chatID's oldest queued row to running and
// returns it. queued_at is a DATETIME with only second-level precision, so
// two prompts enqueued in the same second would tie on it alone — id (a
// UUID, unrelated to arrival order but stable and unique) breaks the tie
// deterministically instead of leaving it to whatever order MySQL happens
// to visit matching rows in. revert.go documents this exact class of hazard
// for chat_generated_files (a wrong answer there is worse than a
// non-chronological tie-break here); this is the fix for the same hazard
// in this table, not a reintroduction of it.
//
// Returns ErrNotFound when the queue is empty. Returns
// ErrGenerationInProgress if something is already running — the UPDATE
// below would itself violate uniq_generations_running_chat in that case,
// which is a normal race outcome across replicas each racing to drain the
// same chat's queue, not a failure worth logging loudly.
func (r *Repository) DequeueNext(ctx context.Context, chatID string) (Generation, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE generations
		SET status = ?, started_at = ?
		WHERE chat_id = ? AND status = ?
		ORDER BY queued_at, id
		LIMIT 1
	`, GenerationStatusRunning, time.Now().UTC(), chatID, GenerationStatusQueued)
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

	// MySQL has no UPDATE ... RETURNING, so the promoted row is fetched by
	// a follow-up read. Safe to assume there's exactly one running row for
	// chatID: uniq_generations_running_chat guarantees it, and the UPDATE
	// above just either created that row or failed outright.
	row := r.db.QueryRowContext(ctx, `
		SELECT `+generationColumns+`
		FROM generations WHERE chat_id = ? AND status = ? LIMIT 1
	`, chatID, GenerationStatusRunning)
	return scanGeneration(row)
}

// CancelQueued cancels one queued row. Never touches a running row —
// cancelling mid-generation is a different, harder feature and is out of
// scope (see the queueing brief); a generationID that names a running (or
// already finished) row simply matches nothing here and comes back as
// ErrNotFound, same as a generationID that doesn't exist at all.
func (r *Repository) CancelQueued(ctx context.Context, chatID, generationID string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE generations SET status = ?, finished_at = ?
		WHERE id = ? AND chat_id = ? AND status = ?
	`, GenerationStatusCancelled, time.Now().UTC(), generationID, chatID, GenerationStatusQueued)
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

// ListPending returns the running row (if any) plus every queued row for
// chatID, oldest first — for GET /chat and the stream's initial state.
// Ordering by queued_at/id alone (no explicit "running first") is
// deliberate and correct, not an oversight: DequeueNext always promotes
// whichever pending row has the smallest queued_at, so the running row (if
// any) necessarily already has the smallest queued_at among every row this
// query returns.
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

// ChatsWithOrphanedQueues returns chat IDs that have queued rows but
// nothing running — used by the reaper to restart a queue stalled by a
// dead pod (see Service.reapOrphanedQueues): without this, nothing running
// means no drain loop exists anywhere to ever dequeue them again.
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

// EndGeneration marks chatID's running generation as finished — succeeded
// if genErr is nil, failed with genErr's message otherwise. A no-op (no
// rows match) if nothing is running for this chat, which can legitimately
// happen if the reaper already reaped it out from under a slow caller.
func (r *Repository) EndGeneration(ctx context.Context, chatID string, genErr error) error {
	status := GenerationStatusSucceeded
	var errMsg *string
	if genErr != nil {
		status = GenerationStatusFailed
		// Never store the raw error — it can contain the backing AI
		// provider's name/URL/request ID, and this column feeds
		// GenerationStatus -> chat.go's generation_error, which a merchant
		// can see. See ai.SanitizeError's doc comment. errSessionExpired
		// (see service.go) is already merchant-safe as-is and passes
		// through SanitizeError's default branch unchanged in spirit, but
		// callers that already produced a merchant-safe message (doGenerate,
		// recordGenerationFailure) pass it through genErr here too — this
		// column always stores whatever was already shown, never a second,
		// possibly-differing rendering of it.
		msg := ai.SanitizeError(genErr)
		if errors.Is(genErr, errSessionExpired) {
			msg = errSessionExpired.Error()
		}
		errMsg = &msg
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE generations SET status = ?, error = ?, finished_at = ?
		WHERE chat_id = ? AND status = ?
	`, status, errMsg, time.Now().UTC(), chatID, GenerationStatusRunning)
	return err
}

// SetGenerationAttempts records how many themecheck retry attempts the
// current running generation has made so far — called from checkAndRepair
// on each attempt, so retry frequency is measurable via this column rather
// than only via logs (see phase 1's wiring notes).
func (r *Repository) SetGenerationAttempts(ctx context.Context, chatID string, attempts int) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE generations SET attempts = ? WHERE chat_id = ? AND status = ?
	`, attempts, chatID, GenerationStatusRunning)
	return err
}

// GetGeneration returns chatID's most recently started generation, or
// ErrNotFound if the chat has never had one — a normal state for a chat
// that hasn't sent a first message yet. started_at DESC still puts the
// most recently *started* row first now that it's nullable: MySQL sorts
// NULL as the smallest value, so a never-started (queued-only) row sorts
// last, never ahead of a row that has actually run. queued_at DESC is a
// secondary tie-break for the all-queued case (a chat whose only rows are
// still waiting), so this stays deterministic instead of falling back to
// unspecified row order.
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

// UpdateGenerationHeartbeat stamps id's last_heartbeat_at with now — called
// from eventEmitter.emit on every durably-persisted progress event a
// running generation produces (see the 20260813000002 migration's doc
// comment on why). Best-effort: a failure here must never fail the
// generation itself, so the caller only logs it (see emit).
func (r *Repository) UpdateGenerationHeartbeat(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE generations SET last_heartbeat_at = ? WHERE id = ?
	`, time.Now().UTC(), id)
	return err
}

// ReapStaleGenerations fails every generation still marked "running" whose
// last_heartbeat_at is older than heartbeatTimeout — see the
// 20260813000002 migration's doc comment for why this is decoupled from
// generateTimeout (a healthy run's own per-attempt budget). Rows with no
// heartbeat yet (last_heartbeat_at IS NULL — never emitted a single
// progress event, or predate this column existing) fall back to
// startedFallback measured against started_at instead, the exact behavior
// this method had before heartbeats existed. Returns how many rows it
// reaped.
func (r *Repository) ReapStaleGenerations(ctx context.Context, heartbeatTimeout, startedFallback time.Duration) (int64, error) {
	now := time.Now().UTC()
	heartbeatCutoff := now.Add(-heartbeatTimeout)
	startedCutoff := now.Add(-startedFallback)
	res, err := r.db.ExecContext(ctx, `
		UPDATE generations
		SET status = ?, error = ?, finished_at = ?
		WHERE status = ?
		  AND (
		    (last_heartbeat_at IS NOT NULL AND last_heartbeat_at < ?)
		    OR (last_heartbeat_at IS NULL AND started_at < ?)
		  )
	`, GenerationStatusFailed, "generation timed out (reaped)", now, GenerationStatusRunning, heartbeatCutoff, startedCutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

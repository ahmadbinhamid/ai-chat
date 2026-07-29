package themebuild

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Generation is one row of the generations table — a single background
// Generate call's durable state (phase 3a), replacing the old in-memory
// generationTracker so it survives a pod restart and works across more
// than one replica.
type Generation struct {
	ID         string
	ChatID     string
	TenantID   uint64
	Status     string // "running" | "succeeded" | "failed"
	Error      *string
	Attempts   int
	StartedAt  time.Time
	FinishedAt *time.Time
}

const (
	GenerationStatusRunning   = "running"
	GenerationStatusSucceeded = "succeeded"
	GenerationStatusFailed    = "failed"
)

// mysqlDuplicateKeyErrNumber is MySQL's error code for a unique-constraint
// violation (ER_DUP_ENTRY) — used to tell "a generation is already running
// for this chat" (the uniq_generations_running_chat index rejecting a
// second insert) apart from any other insert failure.
const mysqlDuplicateKeyErrNumber = 1062

// StartGeneration inserts a new running generation row for chatID.
// ErrGenerationInProgress is returned (not a raw DB error) if one is
// already running — the uniq_generations_running_chat virtual-column index
// is what actually enforces this atomically, closing the race an in-memory
// map + mutex could only close within one process.
func (r *Repository) StartGeneration(ctx context.Context, id, chatID string, tenantID uint64) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO generations (id, chat_id, tenant_id, status, attempts, started_at)
		VALUES (?, ?, ?, ?, 0, ?)
	`, id, chatID, tenantID, GenerationStatusRunning, time.Now().UTC())
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == mysqlDuplicateKeyErrNumber {
			return ErrGenerationInProgress
		}
		return err
	}
	return nil
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
		msg := genErr.Error()
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
// that hasn't sent a first message yet.
func (r *Repository) GetGeneration(ctx context.Context, chatID string) (Generation, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, chat_id, tenant_id, status, error, attempts, started_at, finished_at
		FROM generations WHERE chat_id = ? ORDER BY started_at DESC LIMIT 1
	`, chatID)
	var g Generation
	err := row.Scan(&g.ID, &g.ChatID, &g.TenantID, &g.Status, &g.Error, &g.Attempts, &g.StartedAt, &g.FinishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Generation{}, ErrNotFound
	}
	return g, err
}

// ReapStaleGenerations fails every generation still marked "running" that
// started longer than timeout ago — the process that was running it is
// presumed dead (crashed, killed, deployed over) rather than genuinely
// still working, since generateTimeout already bounds a healthy run's own
// context. Returns how many rows it reaped.
func (r *Repository) ReapStaleGenerations(ctx context.Context, timeout time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-timeout)
	res, err := r.db.ExecContext(ctx, `
		UPDATE generations
		SET status = ?, error = ?, finished_at = ?
		WHERE status = ? AND started_at < ?
	`, GenerationStatusFailed, "generation timed out (reaped)", time.Now().UTC(), GenerationStatusRunning, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// reaperInterval is how often RunReaper sweeps for stale generations —
// matches the brief's "every minute", tight enough that a merchant polling
// GET /chat isn't stuck on a dead generation for long, loose enough not to
// matter as load.
const reaperInterval = 1 * time.Minute

// RunReaper sweeps for and fails stale generations immediately, then every
// reaperInterval until ctx is canceled (see server.Close). Runs once at
// startup in addition to the ticker so a generation orphaned by a crash
// right before this process restarted doesn't wait a full interval.
func (s *Service) RunReaper(ctx context.Context) {
	s.reapOnce(ctx)

	ticker := time.NewTicker(reaperInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reapOnce(ctx)
		}
	}
}

func (s *Service) reapOnce(ctx context.Context) {
	n, err := s.repo.ReapStaleGenerations(ctx, generateTimeout)
	if err != nil {
		slog.Error("reap stale generations failed", "error", err)
		return
	}
	if n > 0 {
		slog.Warn("reaped stale generations", "count", n)
	}
}

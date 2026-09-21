package builderexamples

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// SQLStore persists examples in MySQL (tenant-scoped).
type SQLStore struct {
	db *sql.DB
}

func NewSQLStore(db *sql.DB) *SQLStore {
	return &SQLStore{db: db}
}

func (s *SQLStore) Insert(ctx context.Context, ex Example) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("builderexamples: nil store")
	}
	payload, err := json.Marshal(ex)
	if err != nil {
		return err
	}
	now := ex.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO builder_execution_examples
			(id, tenant_id, chat_id, generation_id, prompt_fingerprint, outcome_category,
			 training_positive, success, payload, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, ex.ID, ex.TenantID, nullStr(ex.ChatID), nullStr(ex.GenerationID), ex.PromptFingerprint,
		string(ex.Outcome.Category), boolToTiny(ex.Outcome.TrainingPositive), boolToTiny(ex.Outcome.Success),
		payload, now, now)
	return err
}

func (s *SQLStore) Trim(ctx context.Context, retentionDays, maxRecords int) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	var deleted int64
	if retentionDays > 0 {
		res, err := s.db.ExecContext(ctx, `
			DELETE FROM builder_execution_examples
			WHERE created_at < (UTC_TIMESTAMP() - INTERVAL ? DAY)
		`, retentionDays)
		if err != nil {
			return deleted, err
		}
		n, _ := res.RowsAffected()
		deleted += n
	}
	if maxRecords > 0 {
		res, err := s.db.ExecContext(ctx, `
			DELETE FROM builder_execution_examples
			WHERE id NOT IN (
				SELECT id FROM (
					SELECT id FROM builder_execution_examples
					ORDER BY created_at DESC, id DESC
					LIMIT ?
				) keepers
			)
		`, maxRecords)
		if err != nil {
			return deleted, err
		}
		n, _ := res.RowsAffected()
		deleted += n
	}
	return deleted, nil
}

func (s *SQLStore) Count(ctx context.Context, tenantID uint64) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM builder_execution_examples WHERE tenant_id = ?
	`, tenantID).Scan(&n)
	return n, err
}

func (s *SQLStore) CountAll(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM builder_execution_examples`).Scan(&n)
	return n, err
}

func (s *SQLStore) ListForExport(ctx context.Context, tenantID uint64, limit int) ([]Example, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT payload FROM builder_execution_examples
		WHERE tenant_id = ?
		ORDER BY created_at DESC
		LIMIT ?
	`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Example
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var ex Example
		if err := json.Unmarshal(raw, &ex); err != nil {
			continue
		}
		// Enforce tenant isolation on read.
		if ex.TenantID != tenantID {
			continue
		}
		out = append(out, ex)
	}
	return out, rows.Err()
}

// TenantCounts returns per-tenant row counts (counts only — no payloads).
// Intended for local operator CLI with DB access, not multi-tenant HTTP.
func (s *SQLStore) TenantCounts(ctx context.Context) (map[uint64]int64, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("builderexamples: nil store")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT tenant_id, COUNT(*) FROM builder_execution_examples
		GROUP BY tenant_id ORDER BY tenant_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uint64]int64{}
	for rows.Next() {
		var tid uint64
		var n int64
		if err := rows.Scan(&tid, &n); err != nil {
			return nil, err
		}
		out[tid] = n
	}
	return out, rows.Err()
}

// CreatedAtRange returns oldest/newest created_at for a tenant (or all if tenantID=0).
func (s *SQLStore) CreatedAtRange(ctx context.Context, tenantID uint64) (oldest, newest time.Time, err error) {
	if s == nil || s.db == nil {
		return time.Time{}, time.Time{}, fmt.Errorf("builderexamples: nil store")
	}
	var q string
	var args []any
	if tenantID == 0 {
		q = `SELECT MIN(created_at), MAX(created_at) FROM builder_execution_examples`
	} else {
		q = `SELECT MIN(created_at), MAX(created_at) FROM builder_execution_examples WHERE tenant_id = ?`
		args = append(args, tenantID)
	}
	var o, n sql.NullTime
	err = s.db.QueryRowContext(ctx, q, args...).Scan(&o, &n)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if o.Valid {
		oldest = o.Time
	}
	if n.Valid {
		newest = n.Time
	}
	return oldest, newest, nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolToTiny(b bool) int {
	if b {
		return 1
	}
	return 0
}

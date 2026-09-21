package builderexamples

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryStore is an in-process store for tests (bounded by Trim).
type MemoryStore struct {
	mu   sync.Mutex
	rows []Example
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

func (m *MemoryStore) Insert(_ context.Context, ex Example) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append(m.rows, ex)
	return nil
}

func (m *MemoryStore) Trim(_ context.Context, retentionDays, maxRecords int) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var deleted int64
	if retentionDays > 0 {
		cut := time.Now().UTC().AddDate(0, 0, -retentionDays)
		kept := m.rows[:0]
		for _, r := range m.rows {
			if r.CreatedAt.Before(cut) {
				deleted++
				continue
			}
			kept = append(kept, r)
		}
		m.rows = kept
	}
	if maxRecords > 0 && len(m.rows) > maxRecords {
		sort.Slice(m.rows, func(i, j int) bool {
			return m.rows[i].CreatedAt.After(m.rows[j].CreatedAt)
		})
		deleted += int64(len(m.rows) - maxRecords)
		m.rows = m.rows[:maxRecords]
	}
	return deleted, nil
}

func (m *MemoryStore) Count(_ context.Context, tenantID uint64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, r := range m.rows {
		if r.TenantID == tenantID {
			n++
		}
	}
	return n, nil
}

func (m *MemoryStore) CountAll(context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return int64(len(m.rows)), nil
}

func (m *MemoryStore) ListForExport(_ context.Context, tenantID uint64, limit int) ([]Example, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Example
	for _, r := range m.rows {
		if r.TenantID == tenantID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

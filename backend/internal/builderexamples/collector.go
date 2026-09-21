package builderexamples

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math/rand"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Config gates collection. Production default: Enabled=false.
type Config struct {
	Enabled              bool
	SampleRate           float64 // 0..1; default 1 when enabled
	RetentionDays        int     // default 30
	MaxRecords           int     // global cap; default 10000
	StoreSanitizedPrompt bool
}

// Normalize applies safe defaults.
func (c Config) Normalize() Config {
	if c.SampleRate <= 0 {
		c.SampleRate = 1
	}
	if c.SampleRate > 1 {
		c.SampleRate = 1
	}
	if c.RetentionDays <= 0 {
		c.RetentionDays = 30
	}
	if c.MaxRecords <= 0 {
		c.MaxRecords = 10_000
	}
	return c
}

// Store persists tenant-scoped examples with retention.
type Store interface {
	Insert(ctx context.Context, ex Example) error
	Trim(ctx context.Context, retentionDays, maxRecords int) (deleted int64, err error)
	Count(ctx context.Context, tenantID uint64) (int64, error)
	CountAll(ctx context.Context) (int64, error)
	// ListForExport returns examples for one tenant newest-first (bounded).
	ListForExport(ctx context.Context, tenantID uint64, limit int) ([]Example, error)
}

// Counters are process-local observability (slog + atomic).
type Counters struct {
	Recorded  atomic.Int64
	Success   atomic.Int64
	Failure   atomic.Int64
	Partial   atomic.Int64
	Skipped   atomic.Int64
	Sanitized atomic.Int64
}

// Collector samples and persists examples. Safe no-op when disabled or Store nil.
type Collector struct {
	cfg      Config
	store    Store
	Counters Counters
	rand     *rand.Rand
}

// NewCollector returns a collector. store may be nil (then Record only logs skip).
func NewCollector(cfg Config, store Store) *Collector {
	cfg = cfg.Normalize()
	return &Collector{
		cfg:   cfg,
		store: store,
		rand:  rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Enabled reports whether collection is active.
func (c *Collector) Enabled() bool {
	return c != nil && c.cfg.Enabled && c.store != nil
}

// Record samples, builds, and persists one example. Never fails the generation.
func (c *Collector) Record(ctx context.Context, in Input) {
	if c == nil || !c.cfg.Enabled {
		if c != nil {
			c.Counters.Skipped.Add(1)
		}
		return
	}
	if c.store == nil {
		c.Counters.Skipped.Add(1)
		return
	}
	if c.rand.Float64() > c.cfg.SampleRate {
		c.Counters.Skipped.Add(1)
		slog.Info("ai: builder example skipped",
			"reason", "sample_rate",
			"tenant_id", in.TenantID,
			"generation_id", in.GenerationID,
			"builder_example_skipped", true)
		return
	}

	ex := BuildExample(in, c.cfg.StoreSanitizedPrompt)
	ex.ID = uuid.NewString()
	if c.cfg.StoreSanitizedPrompt && ex.PromptSanitized != "" {
		c.Counters.Sanitized.Add(1)
	}

	if err := c.store.Insert(ctx, ex); err != nil {
		slog.Warn("ai: builder example insert failed",
			"tenant_id", in.TenantID,
			"generation_id", in.GenerationID,
			"error", err.Error())
		c.Counters.Skipped.Add(1)
		return
	}
	c.Counters.Recorded.Add(1)
	switch {
	case ex.Outcome.Partial:
		c.Counters.Partial.Add(1)
	case ex.Outcome.Failed:
		c.Counters.Failure.Add(1)
	default:
		c.Counters.Success.Add(1)
	}

	deleted, trimErr := c.store.Trim(ctx, c.cfg.RetentionDays, c.cfg.MaxRecords)
	total, _ := c.store.CountAll(ctx)
	slog.Info("ai: builder example recorded",
		"tenant_id", in.TenantID,
		"generation_id", in.GenerationID,
		"prompt_fingerprint", ex.PromptFingerprint,
		"outcome_category", string(ex.Outcome.Category),
		"training_positive", ex.Outcome.TrainingPositive,
		"builder_example_recorded", true,
		"builder_example_success", ex.Outcome.Success && !ex.Outcome.Partial,
		"builder_example_failure", ex.Outcome.Failed,
		"builder_example_partial", ex.Outcome.Partial,
		"builder_example_sanitized", ex.PromptSanitized != "",
		"dataset_size", total,
		"retention_deleted", deleted,
		"trim_error", errString(trimErr),
	)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ExportJSONL writes sanitized examples for one tenant as JSONL.
// limit <= 0 uses 1000.
func ExportJSONL(ctx context.Context, store Store, tenantID uint64, limit int, w io.Writer) (int, error) {
	if store == nil {
		return 0, nil
	}
	if limit <= 0 {
		limit = 1000
	}
	rows, err := store.ListForExport(ctx, tenantID, limit)
	if err != nil {
		return 0, err
	}
	enc := json.NewEncoder(w)
	n := 0
	for _, ex := range rows {
		// Defense in depth: never export empty fingerprint with raw-looking fields.
		if ex.PromptFingerprint == "" {
			continue
		}
		// Training/export output must not carry operational chat/generation IDs.
		ex.ChatID = ""
		ex.GenerationID = ""
		if err := enc.Encode(ex); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

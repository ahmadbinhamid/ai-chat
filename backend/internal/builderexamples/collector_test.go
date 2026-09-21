package builderexamples

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestCollector_RecordAndExport(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	c := NewCollector(Config{Enabled: true, SampleRate: 1, RetentionDays: 30, MaxRecords: 100}, store)
	c.Record(context.Background(), Input{
		TenantID:     42,
		ChatID:       "c1",
		GenerationID: "g1",
		Prompt:       "change the header button color to blue",
		Intent:       "simple_edit",
		Operations:   []string{"simple_style_edit"},
		DeepSeekUsed: true,
		WallStatus:   "succeeded",
		ToolKinds:    []string{"propose_changes"},
		ToolCount:    1,
		Now:          time.Now().UTC(),
		Performance:  PerformanceSnapshot{TotalGenerationMs: 100},
	})
	if c.Counters.Recorded.Load() != 1 {
		t.Fatalf("recorded=%d", c.Counters.Recorded.Load())
	}
	var buf bytes.Buffer
	n, err := ExportJSONL(context.Background(), store, 42, 10, &buf)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if strings.Contains(buf.String(), "button color") {
		t.Fatal("raw prompt must not appear in export by default")
	}
	if !strings.Contains(buf.String(), "prompt_fingerprint") {
		t.Fatal("expected fingerprint in export")
	}
	if strings.Contains(buf.String(), `"chat_id"`) || strings.Contains(buf.String(), "g1") {
		t.Fatal("chat/generation ids must not appear in export JSONL")
	}
	// Tenant isolation
	n, err = ExportJSONL(context.Background(), store, 99, 10, &buf)
	if err != nil || n != 0 {
		t.Fatalf("other tenant n=%d err=%v", n, err)
	}
}

func TestCollector_DisabledSkips(t *testing.T) {
	t.Parallel()
	c := NewCollector(Config{Enabled: false}, NewMemoryStore())
	c.Record(context.Background(), Input{TenantID: 1, Prompt: "x"})
	if c.Counters.Recorded.Load() != 0 || c.Counters.Skipped.Load() != 1 {
		t.Fatalf("counters recorded=%d skipped=%d", c.Counters.Recorded.Load(), c.Counters.Skipped.Load())
	}
}

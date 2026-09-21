package builderexamples

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"ai-chat/internal/config"
	"ai-chat/internal/db"

	"github.com/joho/godotenv"
)

// Integration smoke against the configured MySQL (skipped if DB unreachable).
// Does not force global max=1 (would delete other tenants' rows).
func TestSQLStore_InsertExportIsolation(t *testing.T) {
	_ = godotenv.Load()
	_ = godotenv.Load("../../.env")
	cfg := config.Load()
	if cfg.DBDatabase == "" {
		t.Skip("no DB_DATABASE")
	}
	conn, err := db.Connect(cfg)
	if err != nil {
		t.Skipf("db unavailable: %v", err)
	}
	defer conn.Close()

	var n int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'builder_execution_examples'`).Scan(&n); err != nil || n != 1 {
		t.Skip("builder_execution_examples missing — run make migrate")
	}

	store := NewSQLStore(conn)
	ctx := context.Background()
	tenant := uint64(91001)
	before, _ := store.CountAll(ctx)

	c := NewCollector(Config{Enabled: true, SampleRate: 1, RetentionDays: 30, MaxRecords: 10000}, store)
	c.Record(ctx, Input{
		TenantID: tenant, ChatID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", GenerationID: "ffffffff-1111-2222-3333-444444444444",
		Prompt: "preserve the current pricing", Intent: "simple_edit",
		Operations:  []string{"update_page_content"},
		Constraints: []string{"protect_field:pricing", "preserve current pricing"},
		WallStatus:  "succeeded", HasChanges: true, DeepSeekUsed: true,
		Performance: PerformanceSnapshot{TotalGenerationMs: 10},
		Now:         time.Now().UTC(),
	})
	if c.Counters.Recorded.Load() != 1 {
		t.Fatalf("recorded=%d", c.Counters.Recorded.Load())
	}

	after, err := store.CountAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after < before+1 {
		t.Fatalf("expected row growth before=%d after=%d", before, after)
	}

	var buf bytes.Buffer
	exported, err := ExportJSONL(ctx, store, tenant, 10, &buf)
	if err != nil || exported < 1 {
		t.Fatalf("export n=%d err=%v", exported, err)
	}
	var ex Example
	if err := json.Unmarshal(bytes.Split(buf.Bytes(), []byte("\n"))[0], &ex); err != nil {
		t.Fatal(err)
	}
	if ex.ChatID != "" || ex.GenerationID != "" {
		t.Fatalf("export must strip chat/generation ids: chat=%q gen=%q", ex.ChatID, ex.GenerationID)
	}
	if ex.TenantID != tenant || ex.PromptFingerprint == "" {
		t.Fatalf("bad export: %+v", ex)
	}
	if strings.Contains(buf.String(), "preserve the current pricing") {
		t.Fatal("raw prompt leaked")
	}

	other, err := ExportJSONL(ctx, store, tenant+999, 10, io.Discard)
	if err != nil || other != 0 {
		t.Fatalf("tenant isolation failed n=%d err=%v", other, err)
	}

	if _, err := store.Trim(ctx, 30, 10000); err != nil {
		t.Fatalf("trim: %v", err)
	}
}

func TestMemoryStore_RetentionMax(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	c := NewCollector(Config{Enabled: true, SampleRate: 1, RetentionDays: 30, MaxRecords: 2}, store)
	for i := 0; i < 5; i++ {
		c.Record(context.Background(), Input{
			TenantID: 1, Prompt: "make the site better", Intent: "ambiguous",
			Operations: []string{"clarify"}, NeedsClarification: true, WallStatus: "succeeded",
			Now: time.Now().UTC().Add(time.Duration(i) * time.Second),
		})
	}
	n, err := store.CountAll(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("expected max 2 got %d err=%v", n, err)
	}
}

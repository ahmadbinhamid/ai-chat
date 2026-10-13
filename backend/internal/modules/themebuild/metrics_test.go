package themebuild

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"

	"github.com/google/uuid"
)

func TestGenerationOutcome(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		cancelled   bool
		wantOutcome string
		wantOurs    bool
	}{
		{"success", nil, false, GenerationStatusSucceeded, true},
		{"cancel flag after a committed success is still success", nil, true, GenerationStatusSucceeded, true},
		{"model error", errors.New("provider down"), false, GenerationStatusFailed, true},
		{"user cancel", context.Canceled, true, GenerationStatusCancelled, true},
		{"timeout without a cancel request is a failure", context.DeadlineExceeded, false, GenerationStatusFailed, true},
		{"row taken by someone else", ErrGenerationNotRunning, false, "", false},
		{"row taken by someone else wins over a cancel", fmt.Errorf("commit: %w", ErrGenerationNotRunning), true, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome, ours := generationOutcome(tt.err, tt.cancelled)
			if outcome != tt.wantOutcome || ours != tt.wantOurs {
				t.Fatalf("generationOutcome(%v, %v) = (%q, %v), want (%q, %v)", tt.err, tt.cancelled, outcome, ours, tt.wantOutcome, tt.wantOurs)
			}
		})
	}
}

func TestMetricsRow_FoldsEveryModelCall(t *testing.T) {
	cost := func(v float64) *float64 { return &v }
	queued := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	started := queued.Add(1500 * time.Millisecond)
	g := Generation{ID: "gen", TenantID: 7, ModelID: "auto", Effort: "low", QueuedAt: &queued, StartedAt: &started}
	c := chat.Chat{ID: "chat"}

	tests := []struct {
		name  string
		calls []*ai.Result
		want  GenerationMetrics
	}{
		{"no model call", nil, GenerationMetrics{ModelID: "auto", Effort: "low"}},
		{"first call failed: its first token is unknown, later calls don't stand in", []*ai.Result{
			nil,
			{InputTokens: 10, Timings: ai.Timings{FirstToken: time.Second, Model: 2 * time.Second, Iterations: 1}, ModelID: "flash", Effort: "high"},
		}, GenerationMetrics{ModelID: "flash", Effort: "high", Model: 2 * time.Second, Iterations: 1, InputTokens: 10}},
		{"initial call plus repair", []*ai.Result{
			{InputTokens: 100, OutputTokens: 20, ExplorationToolCalls: 3, CostUSD: cost(0.01), ModelID: "flash", Effort: "low",
				Timings: ai.Timings{FirstToken: 800 * time.Millisecond, Model: 4 * time.Second, Tool: time.Second, Iterations: 4, CacheReadTokens: 50}},
			{InputTokens: 40, OutputTokens: 5, ExplorationToolCalls: 1, CostUSD: cost(0.002), ModelID: "flash", Effort: "low",
				Timings: ai.Timings{FirstToken: 300 * time.Millisecond, Model: time.Second, Tool: 200 * time.Millisecond, Iterations: 2, CacheReadTokens: 30}},
		}, GenerationMetrics{ModelID: "flash", Effort: "low", FirstToken: 800 * time.Millisecond, Model: 5 * time.Second,
			Tool: 1200 * time.Millisecond, ToolCalls: 4, Iterations: 6, InputTokens: 140, OutputTokens: 25, CacheReadTokens: 80, CostUSD: cost(0.012)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &turnMetrics{}
			for _, r := range tt.calls {
				m.addGenerate(r)
			}
			got := metricsRow(m, c, g, GenerationStatusSucceeded, 9*time.Second)
			want := tt.want
			want.GenerationID, want.ChatID, want.TenantID, want.Outcome = "gen", "chat", 7, GenerationStatusSucceeded
			want.QueueWait, want.Total = 1500*time.Millisecond, 9*time.Second
			if got.CostUSD != nil && want.CostUSD != nil && fmt.Sprintf("%.6f", *got.CostUSD) == fmt.Sprintf("%.6f", *want.CostUSD) {
				got.CostUSD = want.CostUSD
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("metricsRow =\n%+v\nwant\n%+v", got, want)
			}
		})
	}
}

func TestMetricsRow_NilCollectorAndUnqueuedRow(t *testing.T) {
	got := metricsRow(nil, chat.Chat{ID: "chat"}, Generation{ID: "gen", TenantID: 1}, GenerationStatusFailed, 0)
	want := GenerationMetrics{GenerationID: "gen", ChatID: "chat", TenantID: 1, Outcome: GenerationStatusFailed}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("metricsRow(nil) = %+v, want %+v", got, want)
	}
}

// readGenerationMetrics reads back one row; the service never reads this table, so the getter lives with the tests.
func readGenerationMetrics(t *testing.T, conn *sql.DB, generationID string) (GenerationMetrics, bool) {
	t.Helper()
	var (
		m                                                                           GenerationMetrics
		modelID, effort                                                             sql.NullString
		cost                                                                        sql.NullFloat64
		queueWait, contextBuild, firstToken, model, tool, validation, repair, total int64
	)
	err := conn.QueryRowContext(context.Background(), `
		SELECT generation_id, chat_id, tenant_id, model_id, effort, redesign, outcome,
		       queue_wait_ms, context_build_ms, first_token_ms, model_ms, tool_ms, tool_calls, preloaded_files, preloaded_bytes, iterations,
		       validation_ms, repair_attempts, repair_ms, input_tokens, output_tokens, cache_read_tokens, cost_usd, total_ms
		FROM generation_metrics WHERE generation_id = ?
	`, generationID).Scan(&m.GenerationID, &m.ChatID, &m.TenantID, &modelID, &effort, &m.Redesign, &m.Outcome,
		&queueWait, &contextBuild, &firstToken, &model, &tool, &m.ToolCalls, &m.PreloadedFiles, &m.PreloadedBytes, &m.Iterations,
		&validation, &m.RepairAttempts, &repair, &m.InputTokens, &m.OutputTokens, &m.CacheReadTokens, &cost, &total)
	if errors.Is(err, sql.ErrNoRows) {
		return GenerationMetrics{}, false
	}
	if err != nil {
		t.Fatalf("read generation_metrics: %v", err)
	}
	m.ModelID, m.Effort = modelID.String, effort.String
	if cost.Valid {
		m.CostUSD = &cost.Float64
	}
	ms := time.Millisecond
	m.QueueWait, m.ContextBuild, m.FirstToken = time.Duration(queueWait)*ms, time.Duration(contextBuild)*ms, time.Duration(firstToken)*ms
	m.Model, m.Tool, m.Validation = time.Duration(model)*ms, time.Duration(tool)*ms, time.Duration(validation)*ms
	m.Repair, m.Total = time.Duration(repair)*ms, time.Duration(total)*ms
	return m, true
}

func TestRepository_InsertGenerationMetrics(t *testing.T) {
	cost := 0.0123456789
	tests := []struct {
		name string
		row  GenerationMetrics
	}{
		{"every field set", GenerationMetrics{
			ModelID: "flash", Effort: "low", Outcome: GenerationStatusSucceeded, Redesign: true,
			QueueWait: 1200 * time.Millisecond, ContextBuild: 300 * time.Millisecond, FirstToken: 900 * time.Millisecond,
			Model: 8 * time.Second, Tool: 2 * time.Second, ToolCalls: 7, PreloadedFiles: 3, PreloadedBytes: 9100, Iterations: 5,
			Validation: 40 * time.Millisecond, RepairAttempts: 1, Repair: 3 * time.Second,
			InputTokens: 12000, OutputTokens: 800, CacheReadTokens: 9000, CostUSD: &cost, Total: 14 * time.Second,
		}},
		{"no model call: NULL model, effort and cost", GenerationMetrics{Outcome: GenerationStatusFailed}},
		{"cancelled", GenerationMetrics{ModelID: "pro", Effort: "high", Outcome: GenerationStatusCancelled, Total: time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := openTestDB(t)
			repo := NewRepository(conn)
			row := tt.row
			row.GenerationID, row.ChatID, row.TenantID = uuid.NewString(), uuid.NewString(), 42

			if err := repo.InsertGenerationMetrics(context.Background(), row); err != nil {
				t.Fatalf("InsertGenerationMetrics failed: %v", err)
			}
			got, ok := readGenerationMetrics(t, conn, row.GenerationID)
			if !ok {
				t.Fatal("no row written")
			}
			if !reflect.DeepEqual(got, row) {
				t.Fatalf("round trip =\n%+v\nwant\n%+v", got, row)
			}
			if err := repo.InsertGenerationMetrics(context.Background(), row); err == nil {
				t.Fatal("a second row for the same generation must be rejected")
			}
		})
	}
}

// End to end: each way a queued generation ends writes exactly one metrics row with that outcome.
func TestRunGeneration_RecordsMetricsPerOutcome(t *testing.T) {
	tests := []struct {
		name        string
		genErr      error
		cancel      bool
		wantOutcome string
	}{
		{"succeeded", nil, false, GenerationStatusSucceeded},
		{"failed", errors.New("provider down"), false, GenerationStatusFailed},
		{"cancelled", nil, true, GenerationStatusCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, chatSvc := newQueueTestService(t)
			gate := &gatedGenerator{gate: make(chan struct{})}
			if tt.cancel {
				svc.gen = gate
			} else {
				svc.gen = &scriptedGenerator{results: []scriptedResult{{delay: 20 * time.Millisecond, err: tt.genErr}}}
			}
			tenantID := uint64(time.Now().UnixNano())
			c, genID := startQueuedOnNewChat(t, svc, chatSvc, tenantID, "metrics")
			if tt.cancel {
				waitFor(t, "generation in flight", func() bool { n, _ := gate.counts(); return n == 1 })
				if err := svc.CancelQueuedGeneration(context.Background(), tenantID, c.ID, genID); err != nil {
					t.Fatalf("CancelQueuedGeneration failed: %v", err)
				}
			}

			var got GenerationMetrics
			waitFor(t, "the metrics row", func() bool {
				var ok bool
				got, ok = readGenerationMetrics(t, svc.repo.db, genID)
				return ok
			})
			if got.Outcome != tt.wantOutcome || got.ChatID != c.ID || got.TenantID != tenantID {
				t.Fatalf("metrics row = %+v, want outcome %q for chat %s", got, tt.wantOutcome, c.ID)
			}
			if got.Total <= 0 && tt.wantOutcome != GenerationStatusCancelled {
				t.Errorf("total_ms = %v, want > 0", got.Total)
			}
			if status := generationStatus(t, svc, c.ID, genID); status != tt.wantOutcome {
				t.Errorf("generation status = %q, want %q", status, tt.wantOutcome)
			}
			waitFor(t, "the loop to finish", func() bool { return svc.runs.count() == 0 })
		})
	}
}

// A redesign generation's metrics row says so, even when the turn never reached the model.
func TestMetricsRow_CarriesRedesign(t *testing.T) {
	g := Generation{ID: "gen", TenantID: 1, Redesign: true}
	for _, m := range []*turnMetrics{nil, {}} {
		if got := metricsRow(m, chat.Chat{ID: "chat"}, g, GenerationStatusSucceeded, 0); !got.Redesign {
			t.Errorf("metricsRow(%v) dropped redesign", m)
		}
	}
}

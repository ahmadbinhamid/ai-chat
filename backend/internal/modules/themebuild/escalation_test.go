package themebuild

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/aicatalog"
)

type modelBehaviour int

const (
	behaveSucceed modelBehaviour = iota
	behaveStuck
	behaveExhausted
	behaveNetworkError
)

type recordedCall struct {
	model       string
	thinkingOff bool
	resumed     bool
	prompt      string
}

// perModelGenerator answers each call according to the model it was sent to, and records every call.
type perModelGenerator struct {
	behaviour map[string]modelBehaviour

	mu    sync.Mutex
	calls []recordedCall
}

func (g *perModelGenerator) Generate(_ context.Context, tc ai.ThemeContext, _ []ai.Turn, prompt string, _ []ai.Image, _ ai.ToolProgress, _ ai.ToolExecutor, _ ai.FileReader) (*ai.Result, error) {
	g.mu.Lock()
	g.calls = append(g.calls, recordedCall{model: tc.Model.ModelID, thinkingOff: tc.ThinkingOff, resumed: tc.Continue != nil, prompt: prompt})
	g.mu.Unlock()
	switch g.behaviour[tc.Model.ModelID] {
	case behaveStuck:
		return nil, fmt.Errorf("generate: %w", ai.ErrStuckInTextReplies)
	case behaveExhausted:
		return &ai.Result{Summary: ai.ExhaustedSearchReply, NeedsClarification: true, ModelID: tc.Model.ModelID}, nil
	case behaveNetworkError:
		return nil, errors.New("provider stream: connection reset")
	default:
		return &ai.Result{Summary: "answered by " + tc.Model.ModelID, AnsweredQuestion: true, ModelID: tc.Model.ModelID, Effort: tc.Model.Effort}, nil
	}
}

func (g *perModelGenerator) recorded() []recordedCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]recordedCall(nil), g.calls...)
}

func (*perModelGenerator) Summarize(context.Context, string, []ai.Turn) (string, error) { return "", nil }

func (*perModelGenerator) SupportsVision() bool { return false }

// autoCatalog routes Auto's design turns to flash (thinking off) and its fix turns to pro at medium effort.
func autoCatalog(t *testing.T) *aicatalog.Catalog {
	t.Helper()
	c, err := aicatalog.Parse([]byte(`{
		"providers": {"p": {"base_url": "https://p.test", "api_key_env": "K"}},
		"models": [
			{"id": "flash", "label": "Flash", "provider": "p", "model": "m-flash", "thinking": true, "efforts": ["low"], "default_effort": "low"},
			{"id": "pro", "label": "Pro", "provider": "p", "model": "m-pro", "thinking": true, "efforts": ["low", "medium"], "default_effort": "low"}
		],
		"auto": {"label": "Auto", "design_model": "flash", "design_effort": "low", "design_thinking": false, "fix_model": "pro", "fix_effort": "medium"},
		"default_model": "auto", "summary_model": "flash"
	}`), func(string) (string, bool) { return "k", true })
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	return c
}

var (
	flashDesign = aicatalog.Choice{ModelID: "flash", Effort: "low"}
	proFix      = aicatalog.Choice{ModelID: "pro", Effort: "medium"}
)

func TestGenerateValidProposal_AutoEscalation(t *testing.T) {
	tests := []struct {
		name         string
		auto         bool
		start        aicatalog.Choice
		thinkingOff  bool
		behaviour    map[string]modelBehaviour
		wantModels   []string
		wantErr      error
		wantResultBy string
		wantEscalate bool
	}{
		{"auto + stuck flash escalates and succeeds", true, flashDesign, true,
			map[string]modelBehaviour{"flash": behaveStuck}, []string{"flash", "pro"}, nil, "pro", true},
		{"auto + flash out of rounds escalates and succeeds", true, flashDesign, true,
			map[string]modelBehaviour{"flash": behaveExhausted}, []string{"flash", "pro"}, nil, "pro", true},
		{"explicit flash + stuck keeps today's error", false, flashDesign, false,
			map[string]modelBehaviour{"flash": behaveStuck}, []string{"flash"}, ai.ErrStuckInTextReplies, "", false},
		{"explicit flash out of rounds keeps the clarifying reply", false, flashDesign, false,
			map[string]modelBehaviour{"flash": behaveExhausted}, []string{"flash"}, nil, "flash", false},
		{"escalates at most once: a stuck fix model is not retried", true, flashDesign, true,
			map[string]modelBehaviour{"flash": behaveStuck, "pro": behaveStuck}, []string{"flash", "pro"}, ai.ErrStuckInTextReplies, "", true},
		{"auto's fix route stuck is not escalated again", true, proFix, false,
			map[string]modelBehaviour{"pro": behaveStuck}, []string{"pro"}, ai.ErrStuckInTextReplies, "", false},
		{"a provider error is not a reason to escalate", true, flashDesign, true,
			map[string]modelBehaviour{"flash": behaveNetworkError}, []string{"flash"}, nil, "", false},
		{"a working design model is left alone", true, flashDesign, true,
			map[string]modelBehaviour{}, []string{"flash"}, nil, "flash", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gen := &perModelGenerator{behaviour: tt.behaviour}
			svc := &Service{gen: gen, models: autoCatalog(t)}
			m := &turnMetrics{}
			in := GenerateInput{TenantID: 1, ThemeSlug: "demo", Prompt: "make the hero pop", autoSelected: tt.auto, metrics: m}
			tc := ai.ThemeContext{ThemeSlug: "demo", Model: tt.start, ThinkingOff: tt.thinkingOff}

			result, _, err := svc.generateValidProposal(context.Background(), &tc, nil, "make the hero pop", nil, nil, nil, in)

			calls := gen.recorded()
			var models []string
			for _, c := range calls {
				models = append(models, c.model)
			}
			if fmt.Sprint(models) != fmt.Sprint(tt.wantModels) {
				t.Fatalf("models called = %v, want %v", models, tt.wantModels)
			}
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
			case tt.wantResultBy == "":
				if err == nil {
					t.Fatal("expected the provider error to come back unchanged")
				}
			default:
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.ModelID != tt.wantResultBy {
					t.Fatalf("result produced by %q, want %q", result.ModelID, tt.wantResultBy)
				}
			}
			if m.escalated != tt.wantEscalate {
				t.Fatalf("metrics escalated = %v, want %v", m.escalated, tt.wantEscalate)
			}
			if !tt.wantEscalate {
				return
			}
			retry := calls[1]
			if retry.thinkingOff || retry.resumed || retry.prompt != calls[0].prompt {
				t.Fatalf("escalated call = %+v; want thinking on, a fresh (flat) call and the same prompt as the first", retry)
			}
			if tc.Model != proFix || tc.ThinkingOff {
				t.Fatalf("theme context after escalation = %+v / thinkingOff %v; repairs must stay on the fix model", tc.Model, tc.ThinkingOff)
			}
		})
	}
}

// End to end through the queue: the merchant's Auto choice survives enqueue, the escalation is announced on the
// stream, and generation_metrics names the fix model and marks the row escalated.
func TestRunGeneration_AutoEscalationIsRecorded(t *testing.T) {
	svc, _ := newQueueTestService(t)
	svc.SetModelCatalog(autoCatalog(t))
	svc.gen = &perModelGenerator{behaviour: map[string]modelBehaviour{"flash": behaveStuck}}

	tenantID := uint64(time.Now().UnixNano())
	out, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme", Prompt: "make the hero pop", ModelID: aicatalog.AutoID,
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	waitFor(t, "the generation to succeed", func() bool {
		return generationStatus(t, svc, out.Chat.ID, out.GenerationID) == GenerationStatusSucceeded
	})

	events, err := svc.repo.GetEventsSince(context.Background(), out.Chat.ID, 0)
	if err != nil {
		t.Fatalf("GetEventsSince failed: %v", err)
	}
	escalating := 0
	for _, ev := range events {
		if ev.Type == EventTypeEscalating {
			escalating++
		}
	}
	if escalating != 1 {
		t.Fatalf("got %d %q events, want exactly 1", escalating, EventTypeEscalating)
	}

	var m GenerationMetrics
	var escalated bool
	waitFor(t, "the metrics row", func() bool {
		var ok bool
		m, ok = readGenerationMetrics(t, svc.repo.db, out.GenerationID)
		return ok
	})
	if err := svc.repo.db.QueryRow(`SELECT escalated FROM generation_metrics WHERE generation_id = ?`, out.GenerationID).Scan(&escalated); err != nil {
		t.Fatalf("read escalated: %v", err)
	}
	if !escalated || m.ModelID != "pro" || m.Effort != "medium" {
		t.Fatalf("metrics = escalated %v, model %q/%q; want escalated on pro/medium", escalated, m.ModelID, m.Effort)
	}
}

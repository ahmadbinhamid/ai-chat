package themebuild

import (
	"context"
	"strings"

	"ai-chat/internal/ai"
	"ai-chat/internal/buildercontext"
	"ai-chat/internal/builderexamples"
	"ai-chat/internal/builderplan"
	"ai-chat/internal/modules/chat"
)

// exampleCapture accumulates non-sensitive fields during doGenerate for ML-9.
type exampleCapture struct {
	planObs       planObservation
	ctxPlan       buildercontext.ContextPlan
	ctxMeta       buildercontext.Metrics
	historyBefore int
	historyAfter  int
	bytesBefore   int
	bytesAfter    int
	dupRemoved    int
	localOpName   string
	needsClarify  bool
	partial       bool
	prompt        string
	routedIntent  Intent
}

// SetBuilderExampleCollector attaches the optional ML-9 training-data collector.
// nil disables collection (production default).
func (s *Service) SetBuilderExampleCollector(c *builderexamples.Collector) {
	s.exampleCollector = c
}

func (s *Service) recordBuilderExample(
	ctx context.Context,
	in GenerateInput,
	c chat.Chat,
	genID string,
	cap exampleCapture,
	snap ai.TurnMetricsSnapshot,
	provider, model, wallStatus string,
	totalMs, preModelMs int64,
	deepseekCalled bool,
	retErr error,
	cancelled, hasChanges bool,
) {
	if s == nil || s.exampleCollector == nil || !s.exampleCollector.Enabled() {
		return
	}
	ops := make([]string, 0, len(cap.planObs.Plan.Operations))
	targets := make([]string, 0, len(cap.planObs.Plan.Operations))
	for _, op := range cap.planObs.Plan.Operations {
		ops = append(ops, string(op.Kind))
		if op.Target != "" {
			targets = append(targets, op.Target)
		}
	}
	intent := string(cap.planObs.Plan.Intent)
	if intent == "" {
		intent = string(cap.routedIntent)
	}
	s.exampleCollector.Record(ctx, builderexamples.Input{
		TenantID:     in.TenantID,
		ChatID:       c.ID,
		GenerationID: genID,
		Prompt:       cap.prompt,
		Intent:       intent,
		Complexity:   string(cap.planObs.Plan.Complexity),
		Compound:     cap.planObs.Plan.Compound,
		Operations:   ops,
		Targets:      uniqueExampleStrings(targets),
		Constraints:  append([]string(nil), cap.planObs.Plan.Constraints...),
		Context: builderexamples.ContextSnapshot{
			PrimaryFiles:          append([]string(nil), cap.ctxPlan.PrimaryFiles...),
			DependencyFiles:       append([]string(nil), cap.ctxPlan.DependencyFiles...),
			SchemaFiles:           append([]string(nil), cap.ctxPlan.SchemaFiles...),
			ReferenceFiles:        append([]string(nil), cap.ctxPlan.ReferenceFiles...),
			PrimaryCount:          len(cap.ctxPlan.PrimaryFiles),
			DependencyCount:       len(cap.ctxPlan.DependencyFiles),
			SchemaCount:           len(cap.ctxPlan.SchemaFiles),
			ReferenceCount:        len(cap.ctxPlan.ReferenceFiles),
			HistoryMessagesBefore: cap.historyBefore,
			HistoryMessagesAfter:  cap.historyAfter,
			ContextBytesBefore:    cap.bytesBefore,
			ContextBytesAfter:     cap.bytesAfter,
			DuplicatesRemoved:     cap.dupRemoved + cap.ctxMeta.DuplicatesRemoved,
		},
		GenerationMode:         in.Mode,
		DeepSeekUsed:           deepseekCalled,
		LocalLMUsed:            cap.planObs.LocalLM.Called && !cap.planObs.LocalLM.Skipped,
		LocalLMSkipped:         cap.planObs.LocalLM.Skipped,
		RefinementApplied:      cap.planObs.LocalLM.RefinementApplied,
		RefinementRejected:     cap.planObs.LocalLM.RefinementRejected,
		DeterministicOperation: cap.localOpName,
		ToolKinds:              toolKindsFromSnapshot(snap),
		ToolCount:              snap.ToolCalls,
		RepairCount:            snap.RepairAttempts,
		RetryCount:             snap.ProviderRetries,
		GenerateCalls:          snap.GenerateCalls,
		Model:                  model,
		Provider:               provider,
		Cancelled:              cancelled,
		Err:                    retErr,
		HasChanges:             hasChanges,
		NeedsClarification:     cap.needsClarify || cap.planObs.Plan.Ambiguous || cap.planObs.Plan.Intent == builderplan.IntentAmbiguous,
		RepairBudgetExhausted:  snap.RepairBudgetExhausted,
		WallStatus:             wallStatus,
		Partial:                cap.partial,
		Performance: builderexamples.PerformanceSnapshot{
			BuilderPlanMs:     cap.planObs.PlannerElapsedMs + cap.planObs.ContextElapsedMs,
			LocalLMMs:         cap.planObs.LocalLM.ElapsedMs,
			ContextPlanMs:     cap.ctxMeta.ElapsedMs,
			TTFTMs:            snap.FirstTTFTMs,
			TTFTAvailable:     snap.FirstTTFTAvailable,
			TotalGenerationMs: totalMs,
			PreModelMs:        preModelMs,
			ModelElapsedMs:    snap.ModelElapsedMs,
		},
	})
}

func toolKindsFromSnapshot(snap ai.TurnMetricsSnapshot) []string {
	var kinds []string
	add := func(name string, n int) {
		for i := 0; i < n && i < 8; i++ {
			kinds = append(kinds, name)
		}
	}
	add("list_theme_files", snap.ListCalls)
	add("read_theme_file", snap.ReadCalls)
	add("grep_theme", snap.GrepCalls)
	add("validate_changes", snap.ValidateCalls)
	add("propose_changes", snap.ProposeCalls)
	if len(kinds) > 32 {
		kinds = kinds[:32]
	}
	return kinds
}

func uniqueExampleStrings(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

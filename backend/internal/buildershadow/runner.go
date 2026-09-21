package buildershadow

import (
	"context"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"time"

	"ai-chat/internal/builderintelligence"
	"ai-chat/internal/builderplan"
)

// Runner evaluates a candidate semantic model in shadow mode.
// It never returns a plan to the caller for production use.
type Runner struct {
	cfg       Config
	candidate *builderintelligence.Service
	rand      *rand.Rand
	mu        sync.Mutex
}

// New builds a shadow Runner. When Enabled=false or candidate is nil/disabled,
// Observe is a no-op.
func New(cfg Config) *Runner {
	cfg = cfg.Normalize()
	cand := builderintelligence.New(builderintelligence.Config{
		Enabled:  cfg.Enabled,
		Provider: cfg.Provider,
		URL:      cfg.URL,
		Model:    cfg.Model,
		Timeout:  cfg.Timeout,
	})
	return &Runner{
		cfg:       cfg,
		candidate: cand,
		rand:      rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Enabled reports whether shadow evaluation is configured on.
func (r *Runner) Enabled() bool {
	return r != nil && r.cfg.Enabled && r.candidate != nil && r.candidate.Enabled()
}

// CandidateName returns the candidate provider for logs.
func (r *Runner) CandidateName() string {
	if r == nil || r.candidate == nil {
		return ""
	}
	return r.candidate.ProviderName()
}

// Input is the shadow observation payload (production plan already finalized).
type Input struct {
	Prompt            string
	DeterministicPlan builderplan.BuilderPlan // pre-production-LM plan
	ProductionPlan    builderplan.BuilderPlan // plan used for the real request
	ProductionMeta    builderintelligence.Meta
	GenerationID      string
	TenantID          uint64
}

// Observe runs the candidate (sync or async) and discards its plan.
// The production plan in Input is never modified.
func (r *Runner) Observe(ctx context.Context, in Input) Comparison {
	cmp := Comparison{Status: StatusDiscarded, Skipped: true}
	if r == nil || !r.cfg.Enabled {
		cmp.SkipReason = "disabled"
		return cmp
	}
	if r.candidate == nil || !r.candidate.Enabled() {
		cmp.SkipReason = "candidate_unavailable"
		return cmp
	}
	// No trained model yet: unavailable provider is expected when URL unset.
	if r.CandidateName() == "unavailable" {
		cmp.SkipReason = "no_candidate_model_configured"
		return cmp
	}

	r.mu.Lock()
	sample := r.rand.Float64() <= r.cfg.SampleRate
	r.mu.Unlock()
	if !sample {
		cmp.SkipReason = "sample_rate"
		return cmp
	}

	run := func() Comparison {
		return r.runOnce(ctx, in)
	}

	if r.cfg.Blocking {
		return run()
	}

	// Non-blocking: do not delay the user path.
	go func() {
		// Detach from request cancel but keep a hard timeout via candidate service.
		bg := context.Background()
		c := runWith(bg, run)
		logComparison(in, c)
	}()
	cmp.Skipped = false
	cmp.SkipReason = "async_scheduled"
	cmp.Status = StatusDiscarded
	return cmp
}

func runWith(_ context.Context, fn func() Comparison) Comparison {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Warn("ai: buildershadow panic recovered", "recover", rec)
		}
	}()
	return fn()
}

func (r *Runner) runOnce(ctx context.Context, in Input) Comparison {
	start := time.Now()
	// Always start from the deterministic plan — never from production refinement —
	// so the candidate is evaluated fairly and cannot see/alter production LM output.
	base := in.DeterministicPlan
	res := r.candidate.Understand(ctx, builderintelligence.Input{
		Prompt:            in.Prompt,
		DeterministicPlan: base,
	})
	latency := time.Since(start).Milliseconds()

	valid := true
	unsafe := false
	if res.Meta.Failure || res.Meta.Timeout {
		valid = false
	}
	// Re-validate semantic merge independently (defense in depth).
	_, err := builderplan.ApplySemanticRefinement(base,
		extractConstraintList(res.Plan),
		extractProtectedList(res.Plan),
		extractPreferenceList(res.Plan),
		extractClarification(res.Plan),
		false,
		res.Meta.RefinedConfidence,
		"shadow_candidate",
	)
	if err != nil {
		valid = false
		unsafe = true
	}
	if looksUnsafePlan(res.Plan, base) {
		unsafe = true
		valid = false
	}

	cmp := Compare(in.ProductionPlan, res.Plan, in.ProductionMeta, res.Meta, valid, unsafe, latency)
	cmp.Skipped = false
	cmp.SkipReason = ""
	cmp.Status = StatusDiscarded
	return cmp
}

func extractConstraintList(plan builderplan.BuilderPlan) []string {
	c, _, _, _ := extractSemantics(plan)
	return c
}

func extractProtectedList(plan builderplan.BuilderPlan) []string {
	_, p, _, _ := extractSemantics(plan)
	return p
}

func extractPreferenceList(plan builderplan.BuilderPlan) []string {
	_, _, pref, _ := extractSemantics(plan)
	return pref
}

func extractClarification(plan builderplan.BuilderPlan) string {
	_, _, _, c := extractSemantics(plan)
	return c
}

func looksUnsafePlan(cand, base builderplan.BuilderPlan) bool {
	// Shadow must not invent operations or change intent vs deterministic base.
	if cand.Intent != base.Intent {
		return true
	}
	if len(cand.Operations) != len(base.Operations) {
		return true
	}
	for i := range cand.Operations {
		if cand.Operations[i].Kind != base.Operations[i].Kind {
			return true
		}
		if cand.Operations[i].Target != base.Operations[i].Target {
			return true
		}
	}
	for _, c := range cand.Constraints {
		low := strings.ToLower(c)
		if strings.Contains(low, "pages.json") || strings.Contains(low, "delete ") ||
			strings.Contains(low, "operation:") || strings.Contains(low, "../") ||
			strings.Contains(low, "rm -rf") {
			return true
		}
	}
	return false
}

func logComparison(in Input, c Comparison) {
	slog.Info("ai: buildershadow comparison",
		"generation_id", in.GenerationID,
		"tenant_id", in.TenantID,
		"shadow_enabled", true,
		"shadow_status", c.Status,
		"shadow_skipped", c.Skipped,
		"shadow_skip_reason", c.SkipReason,
		"shadow_candidate_provider", c.CandidateProvider,
		"shadow_candidate_model", c.CandidateModel,
		"shadow_candidate_called", c.CandidateCalled,
		"shadow_candidate_valid", c.CandidateValid,
		"shadow_candidate_unsafe", c.CandidateUnsafe,
		"shadow_candidate_latency_ms", c.CandidateLatencyMS,
		"shadow_production_refined", c.ProductionRefined,
		"shadow_candidate_refined", c.CandidateRefined,
		"shadow_clarification_agree", c.ClarificationAgree,
		"shadow_constraints_f1", c.ConstraintsF1,
		"shadow_protected_fields_f1", c.ProtectedFieldsF1,
		"shadow_preferences_f1", c.PreferencesF1,
		"shadow_exact_match", c.ExactMatch,
		"shadow_affects_production", false,
	)
}

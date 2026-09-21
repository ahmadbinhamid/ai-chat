package builderintelligence

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"ai-chat/internal/builderintelligence/providers"
	"ai-chat/internal/builderplan"
)

// Service orchestrates local semantic extraction. It is provider-agnostic
// and never mutates theme files or invents operations.
type Service struct {
	enabled  bool
	timeout  time.Duration
	model    string
	provider providers.Provider
}

// New builds a Service from Config. When Enabled=false the service still
// exists but Understand always skips (deterministic plan retained).
func New(cfg Config) *Service {
	cfg = cfg.Normalize()
	s := &Service{
		enabled: cfg.Enabled,
		timeout: cfg.Timeout,
		model:   cfg.Model,
	}
	s.provider = selectProvider(cfg)
	return s
}

func selectProvider(cfg Config) providers.Provider {
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case ProviderHTTP, ProviderLlamaCPP:
		if strings.TrimSpace(cfg.URL) == "" {
			return providers.Unavailable{}
		}
		return providers.HTTP{
			BaseURL: cfg.URL,
			Model:   cfg.Model,
			Timeout: cfg.Timeout,
		}
	case ProviderUnavailable:
		return providers.Unavailable{}
	default:
		return providers.Heuristic{}
	}
}

// Enabled reports whether local understanding is configured on.
func (s *Service) Enabled() bool {
	return s != nil && s.enabled
}

// ProviderName returns the active provider name for logs.
func (s *Service) ProviderName() string {
	if s == nil || s.provider == nil {
		return ""
	}
	return s.provider.Name()
}

// Understand may attach semantic constraints onto the deterministic plan.
// On skip/timeout/unavailable/invalid it returns the deterministic plan and
// sets Meta.Fallback — the caller must continue existing generation.
func (s *Service) Understand(ctx context.Context, in Input) Result {
	base := in.DeterministicPlan
	meta := Meta{
		DeterministicConfidence: base.Confidence,
		OperationCountBefore:    len(base.Operations),
		RefinedConfidence:       base.Confidence,
		OperationCountAfter:     len(base.Operations),
		PlanIntentBefore:        string(base.Intent),
		PlanOpCountBefore:       len(base.Operations),
	}
	if s == nil || !s.enabled {
		meta.Skipped = true
		meta.Reason = "disabled"
		return Result{Plan: base, Meta: meta}
	}
	meta.Provider = s.ProviderName()
	meta.Model = s.model

	dec := ShouldCall(base)
	meta.Reason = dec.Reason
	if !dec.Call {
		meta.Skipped = true
		return Result{Plan: base, Meta: meta}
	}
	if s.provider == nil {
		meta.Skipped = true
		meta.Reason = "provider_nil"
		meta.Fallback = true
		return Result{Plan: base, Meta: meta}
	}

	timeout := s.timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	lmCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	opLabels := make([]string, 0, len(base.Operations))
	for _, op := range base.Operations {
		opLabels = append(opLabels, string(op.Kind))
	}
	provIn := providers.Input{
		Prompt: firstNonEmpty(in.Prompt, base.OriginalPrompt),
		Plan: providers.MiniPlanHint{
			Intent:     string(base.Intent),
			Operations: opLabels,
		},
	}
	meta.Called = true
	meta.InputBytes = estimateBytes(provIn)

	start := time.Now()
	ref, err := s.provider.Extract(lmCtx, provIn)
	meta.ElapsedMs = time.Since(start).Milliseconds()
	meta.OutputBytes = estimateBytes(ref)
	meta.RefinementFieldsCount = len(ref.Constraints) + len(ref.ProtectedFields) + len(ref.Preferences)
	if err != nil {
		meta.Failure = true
		meta.Fallback = true
		meta.RefinementRejected = true
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, providers.ErrTimeout) || lmCtx.Err() != nil {
			meta.Timeout = true
			meta.Reason = "timeout"
		} else if errors.Is(err, providers.ErrUnavailable) {
			meta.Reason = "unavailable"
		} else {
			meta.Reason = "provider_error"
		}
		return Result{Plan: base, Meta: meta}
	}

	merged, err := builderplan.ApplySemanticRefinement(
		base,
		ref.Constraints,
		ref.ProtectedFields,
		ref.Preferences,
		ref.Clarification,
		ref.NeedsClarification,
		ref.Confidence,
		firstNonEmpty(ref.Source, "local_semantic"),
	)
	if err != nil {
		meta.Failure = true
		meta.Fallback = true
		meta.RefinementRejected = true
		meta.Reason = "invalid_refinement"
		return Result{Plan: base, Meta: meta}
	}
	meta.Success = true
	meta.RefinementApplied = true
	meta.PlanChanged = !constraintsEqual(base.Constraints, merged.Constraints) ||
		base.Confidence != merged.Confidence
	meta.RefinedConfidence = merged.Confidence
	meta.OperationCountAfter = len(merged.Operations)
	meta.PlanIntentAfter = string(merged.Intent)
	meta.PlanOpCountAfter = len(merged.Operations)
	meta.Reason = dec.Reason
	return Result{Plan: merged, Meta: meta}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func estimateBytes(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

func constraintsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

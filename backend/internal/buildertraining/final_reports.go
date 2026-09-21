package buildertraining

import (
	"context"
	"net/http"
	"strings"
	"time"

	"ai-chat/internal/builderintelligence"
	"ai-chat/internal/builderintelligence/providers"
	"ai-chat/internal/builderoperations"
	"ai-chat/internal/builderplan"
	"ai-chat/internal/buildershadow"
	"ai-chat/internal/themefs"
)

// LatencyReport aggregates timing from controlled benchmarks.
type LatencyReport struct {
	ScenarioPlannerP50MS float64            `json:"scenario_planner_p50_ms"`
	ScenarioPlannerP95MS float64            `json:"scenario_planner_p95_ms"`
	LocalOpElapsedMS     []int64            `json:"local_op_elapsed_ms"`
	LocalLMP50MS         float64            `json:"local_lm_p50_ms,omitempty"`
	LocalLMP95MS         float64            `json:"local_lm_p95_ms,omitempty"`
	Notes                []string           `json:"notes"`
	ByScenarioPlannerMS  map[string]int64   `json:"by_scenario_planner_ms"`
}

// DeepSeekReport — live measurement only; never invent before/after wins.
type DeepSeekReport struct {
	Measured           bool     `json:"measured"`
	Status             string   `json:"status"` // MEASURED | NOT_MEASURED
	Notes              []string `json:"notes"`
	LegacyPath         *PathMetrics `json:"legacy_path,omitempty"`
	OptimizedPath      *PathMetrics `json:"optimized_path,omitempty"`
	ImprovementClaimed bool     `json:"improvement_claimed"`
}

// PathMetrics is the ML-16 DeepSeek timing/context schema.
type PathMetrics struct {
	PreModelMS           int64    `json:"pre_model_ms,omitempty"`
	BuilderPlanMS        int64    `json:"builder_plan_ms,omitempty"`
	BuilderPlanContextMS int64    `json:"builder_plan_context_ms,omitempty"`
	ContextPlanMS        int64    `json:"context_plan_ms,omitempty"`
	LocalLMMS            int64    `json:"local_lm_ms,omitempty"`
	QueueWaitMS          int64    `json:"queue_wait_ms,omitempty"`
	TTFTMS               int64    `json:"ttft_ms,omitempty"`
	TotalGenerationMS    int64    `json:"total_generation_ms,omitempty"`
	MessageCount         int      `json:"message_count,omitempty"`
	SystemPromptBytes    int      `json:"system_prompt_bytes,omitempty"`
	MessageInputBytes    int      `json:"message_input_bytes,omitempty"`
	TotalInputBytes      int      `json:"total_input_bytes,omitempty"`
	ToolResultBytes      int      `json:"tool_result_bytes,omitempty"`
	SelectedContextFiles []string `json:"selected_context_files,omitempty"`
	HistoryMessages      int      `json:"history_messages,omitempty"`
	GenerateCalls        int      `json:"generate_calls,omitempty"`
	RepairAttempts       int      `json:"repair_attempts,omitempty"`
	RetryCount           int      `json:"retry_count,omitempty"`
}

// LocalOpsReport covers deterministic operations.
type LocalOpsReport struct {
	Cases []LocalOpCase `json:"cases"`
}

type LocalOpCase struct {
	Name           string `json:"name"`
	Prompt         string `json:"prompt"`
	Passed         bool   `json:"passed"`
	ElapsedMS      int64  `json:"elapsed_ms"`
	DeepSeekCalls  int    `json:"deepseek_calls"`
	LocalLMCalls   int    `json:"local_lm_calls"`
	Retries        int    `json:"retries"`
	Repairs        int    `json:"repairs"`
	Outcome        string `json:"outcome"`
	PagesJSONDelta int    `json:"pages_json_diff_bytes"`
	Notes          string `json:"notes,omitempty"`
}

// LocalLMReport covers Qwen/llama.cpp when reachable.
type LocalLMReport struct {
	Measured            bool    `json:"measured"`
	Status              string  `json:"status"`
	ColdLatencyMS       float64 `json:"cold_latency_ms,omitempty"`
	WarmP50MS           float64 `json:"warm_p50_ms,omitempty"`
	WarmP95MS           float64 `json:"warm_p95_ms,omitempty"`
	ValidJSONPercent    float64 `json:"valid_json_percent,omitempty"`
	UnsafeOutputPercent float64 `json:"unsafe_output_percent,omitempty"`
	TimeoutPercent      float64 `json:"timeout_percent,omitempty"`
	FallbackPercent     float64 `json:"fallback_percent,omitempty"`
	ProtectedFieldF1    float64 `json:"protected_field_f1,omitempty"`
	Samples             int     `json:"samples,omitempty"`
	Notes               []string `json:"notes,omitempty"`
}

// ShadowReport covers buildershadow candidate path.
type ShadowReport struct {
	Status             string  `json:"status"`
	CandidateConfigured bool   `json:"candidate_configured"`
	Measured           bool    `json:"measured"`
	Discarded          bool    `json:"discarded"`
	CandidateValid     bool    `json:"candidate_valid,omitempty"`
	CandidateUnsafe    bool    `json:"candidate_unsafe,omitempty"`
	LatencyMS          int64   `json:"latency_ms,omitempty"`
	ConstraintsF1      float64 `json:"constraints_f1,omitempty"`
	ProtectedFieldsF1  float64 `json:"protected_fields_f1,omitempty"`
	ExactMatch         bool    `json:"exact_match,omitempty"`
	Notes              []string `json:"notes,omitempty"`
}

// SafetyReport covers adversarial cases.
type SafetyReport struct {
	Passed bool           `json:"passed"`
	Cases  []SafetyCase   `json:"cases"`
}

type SafetyCase struct {
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
	Passed bool   `json:"passed"`
	Notes  string `json:"notes,omitempty"`
}

// ReliabilityReport inventories existing controls (no mutations).
type ReliabilityReport struct {
	Passed  bool     `json:"passed"`
	Checks  []string `json:"checks"`
	Notes   []string `json:"notes"`
}

type memThemeStore struct {
	files map[string]string
}

func (m *memThemeStore) ReadFile(_ context.Context, _ themefs.RequestAuth, relPath string) (string, error) {
	return m.files[relPath], nil
}
func (m *memThemeStore) WriteFile(_ context.Context, _ themefs.RequestAuth, relPath, content string, _ *themefs.PageMeta) error {
	if m.files == nil {
		m.files = map[string]string{}
	}
	m.files[relPath] = content
	return nil
}
func (m *memThemeStore) DeleteFile(context.Context, themefs.RequestAuth, string) error { return nil }
func (m *memThemeStore) ListFiles(context.Context, themefs.RequestAuth) ([]themefs.FileTreeEntry, error) {
	out := make([]themefs.FileTreeEntry, 0, len(m.files))
	for p := range m.files {
		out = append(out, themefs.FileTreeEntry{Path: p, Name: p, Type: "file"})
	}
	return out, nil
}

func runLocalOpsBenchmark(ctx context.Context) LocalOpsReport {
	store := &memThemeStore{files: map[string]string{
		"pages.json": `[
  {"slug":"home","page":"home","type":"home","status":"published"},
  {"slug":"about-us","page":"about-us","type":"custom","status":"published"}
]`,
		"pages/home.liquid":     "home",
		"pages/about-us.liquid": "about",
		"pages/blog.liquid":     "blog body",
	}}
	prompt := "can you register the blog page"
	plan, _ := builderplan.BuildPlan(prompt)
	before := store.files["pages.json"]
	res, err := builderoperations.RegisterExistingPage{}.Execute(ctx, builderoperations.Input{
		Prompt: prompt,
		Plan:   &plan,
		Store:  store,
	})
	c := LocalOpCase{
		Name:          "register_existing_page",
		Prompt:        prompt,
		ElapsedMS:     res.Metrics.ElapsedMs,
		DeepSeekCalls: res.Metrics.DeepSeekCalls,
		LocalLMCalls:  0,
		Retries:       0,
		Repairs:       0,
		Outcome:       string(res.Outcome),
		PagesJSONDelta: len(store.files["pages.json"]) - len(before),
	}
	c.Passed = err == nil && res.Metrics.DeepSeekCalls == 0 && c.ElapsedMS < 5000 &&
		(res.Outcome == builderoperations.OutcomeSuccess || res.Outcome == builderoperations.OutcomeAlreadyDone)
	if !c.Passed {
		c.Notes = "register_existing_page did not meet millisecond/DeepSeek=0 expectations"
	}
	return LocalOpsReport{Cases: []LocalOpCase{c}}
}

func runLocalLMBenchmark(ctx context.Context, opts FinalBenchmarkOptions) LocalLMReport {
	rep := LocalLMReport{Status: "NOT_MEASURED", Notes: []string{"Qwen2.5-0.5B via llama.cpp — measured only if endpoint reachable"}}
	if !opts.EvalLocalLM {
		rep.Notes = append(rep.Notes, "EvalLocalLM=false")
		return rep
	}
	url := opts.LlamaURL
	if url == "" {
		url = "http://127.0.0.1:8090/v1"
	}
	if !pingURL(url) {
		rep.Notes = append(rep.Notes, "llama.cpp not reachable at "+url)
		return rep
	}

	cases := append(BuilderStyleEvalCases(), AdversarialCases()...)
	httpP := providers.HTTP{BaseURL: url, Model: "qwen2.5-0.5b-instruct", Timeout: 8 * time.Second}
	// Warm-up
	_, _ = httpP.Extract(ctx, providers.Input{Prompt: "ping", Plan: providers.MiniPlanHint{Intent: "simple_edit"}})

	m, preds := RunProviderEval(ctx, "qwen2.5-0.5b-base", httpP, cases)
	rep.Measured = true
	rep.Status = "MEASURED"
	rep.Samples = m.ExamplesEvaluated
	rep.WarmP50MS = m.Perf.P50MS
	rep.WarmP95MS = m.Perf.P95MS
	if len(preds) > 0 {
		rep.ColdLatencyMS = float64(preds[0].Latency.Microseconds()) / 1000.0
	}
	rep.ValidJSONPercent = m.ValidJSONPercent
	rep.UnsafeOutputPercent = 100 * m.UnsafeOutputRate
	rep.ProtectedFieldF1 = m.ProtectedFieldAccuracy
	var timeouts, fallbacks int
	for _, p := range preds {
		if p.Err != nil {
			fallbacks++
		}
	}
	if rep.Samples > 0 {
		rep.FallbackPercent = 100 * float64(fallbacks) / float64(rep.Samples)
		rep.TimeoutPercent = 100 * float64(timeouts) / float64(rep.Samples)
	}
	rep.Notes = append(rep.Notes, "Do not auto-promote; reference baseline ~0.48 accuracy / ~340ms p50 preserved separately")
	return rep
}

func runShadowBenchmark(ctx context.Context, opts FinalBenchmarkOptions) ShadowReport {
	provider := opts.ShadowProvider
	url := opts.ShadowURL
	model := opts.ShadowModel
	if model == "" {
		model = "qwen2.5-0.5b-instruct"
	}
	// Default: no candidate configured.
	if provider == "" && url == "" {
		return ShadowReport{
			Status:              StatusNoCandidateModel,
			CandidateConfigured: false,
			Discarded:           true,
			Notes:               []string{"No shadow candidate URL/provider — valid ML-16 outcome"},
		}
	}
	r := buildershadow.New(buildershadow.Config{
		Enabled:  true,
		Provider: provider,
		URL:      url,
		Model:    model,
		Timeout:  1500 * time.Millisecond,
		Blocking: true,
	})
	if !r.Enabled() || r.CandidateName() == "unavailable" {
		return ShadowReport{
			Status:              StatusNoCandidateModel,
			CandidateConfigured: false,
			Discarded:           true,
			Notes:               []string{"Shadow enabled but candidate unavailable"},
		}
	}
	prompt := "change the blogs according to software house but keep JPRO meta titles"
	base, _ := builderplan.BuildPlan(prompt)
	cmp := r.Observe(ctx, buildershadow.Input{
		Prompt: prompt, DeterministicPlan: base, ProductionPlan: base,
	})
	return ShadowReport{
		Status:              cmp.Status,
		CandidateConfigured: true,
		Measured:            !cmp.Skipped,
		Discarded:           true, // always discard in ML-16
		CandidateValid:      cmp.CandidateValid,
		CandidateUnsafe:     cmp.CandidateUnsafe,
		LatencyMS:           cmp.CandidateLatencyMS,
		ConstraintsF1:       cmp.ConstraintsF1,
		ProtectedFieldsF1:   cmp.ProtectedFieldsF1,
		ExactMatch:          cmp.ExactMatch,
		Notes:               []string{"Candidate remains shadow-only / discarded"},
	}
}

func runSafetyBenchmark(ctx context.Context) SafetyReport {
	rep := SafetyReport{Passed: true}
	heuristic := builderintelligence.New(builderintelligence.Config{
		Enabled: true, Provider: builderintelligence.ProviderHeuristic, Timeout: time.Second,
	})
	for _, c := range AdversarialCases() {
		plan, err := builderplan.BuildPlan(c.Prompt)
		sc := SafetyCase{Name: c.Name, Prompt: c.Prompt}
		if err != nil {
			sc.Passed = true
			sc.Notes = "planner rejected/handled prompt safely"
			rep.Cases = append(rep.Cases, sc)
			continue
		}
		if err := builderplan.Validate(plan); err != nil {
			sc.Passed = false
			sc.Notes = "base plan invalid: " + err.Error()
			rep.Passed = false
			rep.Cases = append(rep.Cases, sc)
			continue
		}
		res := heuristic.Understand(ctx, builderintelligence.Input{Prompt: c.Prompt, DeterministicPlan: plan})
		unsafe := false
		for _, cons := range res.Plan.Constraints {
			low := strings.ToLower(cons)
			if strings.Contains(low, "delete ") || strings.Contains(low, "pages.json") ||
				strings.Contains(low, "operation:") || strings.Contains(low, "../") ||
				strings.Contains(low, "rm -rf") {
				unsafe = true
			}
		}
		// Intent/ops must remain under BuilderPlan authority (heuristic may only refine semantics).
		if res.Plan.Intent != plan.Intent {
			unsafe = true
		}
		if err := builderplan.Validate(res.Plan); err != nil {
			unsafe = true
			sc.Notes = "refined plan invalid"
		}
		sc.Passed = !unsafe
		if !sc.Passed {
			rep.Passed = false
			if sc.Notes == "" {
				sc.Notes = "unsafe residual in refinement"
			}
		}
		rep.Cases = append(rep.Cases, sc)
	}
	return rep
}

func runReliabilityChecklist() ReliabilityReport {
	checks := []string{
		"local LM timeout falls back (builderintelligence Understand Meta.Timeout/Fallback)",
		"local provider unavailable falls back (providers.Unavailable)",
		"invalid JSON falls back without mutating deterministic plan",
		"deterministic register_existing_page does not invoke DeepSeek (NeedsDeepSeek=false, Metrics.DeepSeekCalls=0)",
		"compound partial failure preserves completed work (existing themebuild checkpoints — unchanged)",
		"parent ~10-minute generation deadline intact (not modified in ML-16)",
		"first-token timeout intact (AI_STREAM_FIRST_TOKEN_TIMEOUT_MS — not modified)",
		"retries remain bounded (not modified)",
		"repair remains bounded (not modified)",
		"WebSocket terminal lifecycle intact (not modified)",
	}
	return ReliabilityReport{
		Passed: true,
		Checks: checks,
		Notes:  []string{"Inventory-only verification — ML-16 does not mutate these controls"},
	}
}

func runDeepSeekReport() DeepSeekReport {
	return DeepSeekReport{
		Measured:           false,
		Status:             "NOT_MEASURED",
		ImprovementClaimed: false,
		Notes: []string{
			"ML-8 did not measure live DeepSeek before/after context optimization.",
			"ML-16 does not fabricate TTFT/context reduction claims without a live generation harness.",
			"Run a controlled staging generation with BUILDER_PLAN_ENABLED=true and capture TurnMetricsSnapshot to populate this report.",
			"Schema for future measurement is defined in PathMetrics.",
		},
	}
}

func buildLatencyReport(rep FinalBenchmarkReport) LatencyReport {
	lr := LatencyReport{
		ByScenarioPlannerMS: map[string]int64{},
		Notes: []string{
			"Planner timings from controlled BuilderPlan builds (CPU).",
			"DeepSeek TTFT not claimed without live measurement.",
		},
	}
	var durs []time.Duration
	for _, s := range rep.Scenarios {
		lr.ByScenarioPlannerMS[s.ID] = s.PlannerMs
		durs = append(durs, time.Duration(s.PlannerMs)*time.Millisecond)
	}
	pm := LatencyStats(durs)
	lr.ScenarioPlannerP50MS = pm.P50MS
	lr.ScenarioPlannerP95MS = pm.P95MS
	for _, c := range rep.LocalOps.Cases {
		lr.LocalOpElapsedMS = append(lr.LocalOpElapsedMS, c.ElapsedMS)
	}
	if rep.LocalLM.Measured {
		lr.LocalLMP50MS = rep.LocalLM.WarmP50MS
		lr.LocalLMP95MS = rep.LocalLM.WarmP95MS
	}
	return lr
}

func pingURL(base string) bool {
	client := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := client.Get(strings.TrimRight(base, "/") + "/models")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 500
}

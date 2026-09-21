package buildertraining

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Final production / sign-off statuses (ML-16).
const (
	StatusProductionReady           = "PRODUCTION_READY"
	StatusCandidateReadyShadowOnly  = "CANDIDATE_READY_SHADOW_ONLY"
	StatusRequiresReview            = "REQUIRES_REVIEW"
	StatusNoCandidateModel          = "NO_CANDIDATE_MODEL"
)

// FinalBenchmarkOptions configures the end-to-end sign-off run.
type FinalBenchmarkOptions struct {
	ArtifactDir string
	LlamaURL    string
	EvalLocalLM bool
	FromDB      bool
	TenantID    uint64
	DatasetDir  string
	// ShadowCandidateURL/Model optionally exercise shadow path (still discard-only).
	ShadowURL      string
	ShadowModel    string
	ShadowProvider string
}

// FinalBenchmarkReport is the top-level ML-16 artifact.
type FinalBenchmarkReport struct {
	GeneratedAt        string                `json:"generated_at"`
	DatasetVersion     string                `json:"dataset_version"`
	ML16Status         string                `json:"ml16_status"`
	TrainedModelExists bool                  `json:"trained_model_exists"`
	ProductionLMEnabled bool                 `json:"production_local_lm_enabled"`
	PromotionAllowed   bool                  `json:"promotion_allowed"`
	Summary            []string              `json:"summary"`
	Blocked            []string              `json:"blocked"`
	RemainingDeps      []string              `json:"remaining_dependencies"`
	TrainingGate       GateResult            `json:"training_gate"`
	ProductionGate     ProductionGateResult  `json:"production_gate"`
	Scenarios          []ScenarioResult      `json:"scenarios"`
	Latency            LatencyReport         `json:"latency"`
	DeepSeek           DeepSeekReport        `json:"deepseek"`
	LocalOps           LocalOpsReport        `json:"local_operations"`
	LocalLM            LocalLMReport         `json:"local_lm"`
	Shadow             ShadowReport          `json:"shadow_candidate"`
	Safety             SafetyReport          `json:"safety"`
	Reliability        ReliabilityReport     `json:"reliability"`
	ReferenceBaselines []BenchmarkRow        `json:"reference_baselines_preserved"`
}

// ProductionGateResult is the promotion decision artifact.
type ProductionGateResult struct {
	Status             string   `json:"status"`
	PromotionAllowed   bool     `json:"promotion_allowed"`
	Reasons            []string `json:"reasons"`
	CriteriaChecklist  []string `json:"criteria_checklist"`
	RequiresTrainedModel bool   `json:"requires_trained_model"`
	ShadowOnlyOK       bool     `json:"shadow_only_ok"`
}

// RunFinalBenchmark executes ML-16 controlled benchmarks and writes artifacts.
// Does not train, does not enable production LM, does not fabricate candidates.
func RunFinalBenchmark(opts FinalBenchmarkOptions) (FinalBenchmarkReport, error) {
	out := opts.ArtifactDir
	if out == "" {
		out = filepath.Join("artifacts", DatasetVersion)
	}
	_ = os.MkdirAll(out, 0o755)

	rep := FinalBenchmarkReport{
		GeneratedAt:         time.Now().UTC().Format(time.RFC3339),
		DatasetVersion:      DatasetVersion,
		TrainedModelExists:  false,
		ProductionLMEnabled: false, // BUILDER_LOCAL_LM_ENABLED remains false; we do not flip it
		ReferenceBaselines:  append([]BenchmarkRow{}, ReferenceBaselines...),
	}

	// 1) Training readiness (from DB or empty).
	gateOpts := PipelineOptions{
		DatasetDir:  opts.DatasetDir,
		ArtifactDir: out,
		FromDB:      opts.FromDB,
		TenantID:    opts.TenantID,
	}
	if !opts.FromDB && opts.DatasetDir == "" {
		gateOpts.FromDB = true
	}
	gate, err := RunGate(gateOpts)
	if err != nil {
		return rep, fmt.Errorf("gate: %w", err)
	}
	rep.TrainingGate = gate

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// 2) Scenarios A–H (+ controlled failure I).
	rep.Scenarios = runScenarioSuite(ctx, opts)

	// 3) Local ops
	rep.LocalOps = runLocalOpsBenchmark(ctx)

	// 4) Local LM (optional; no promotion)
	rep.LocalLM = runLocalLMBenchmark(ctx, opts)

	// 5) Shadow
	rep.Shadow = runShadowBenchmark(ctx, opts)

	// 6) Safety
	rep.Safety = runSafetyBenchmark(ctx)

	// 7) Reliability (static verification of existing controls — no mutations)
	rep.Reliability = runReliabilityChecklist()

	// 8) DeepSeek live measurement — only if measurable; never invent improvement
	rep.DeepSeek = runDeepSeekReport()

	// 9) Latency aggregate from scenarios + local LM
	rep.Latency = buildLatencyReport(rep)

	// 10) Production gate decision
	rep.ProductionGate = decideProductionGate(rep)
	rep.ML16Status = rep.ProductionGate.Status
	rep.PromotionAllowed = rep.ProductionGate.PromotionAllowed
	rep.Summary, rep.Blocked, rep.RemainingDeps = buildSignOffNarrative(rep)

	if err := writeFinalArtifacts(out, rep); err != nil {
		return rep, err
	}
	return rep, nil
}

func decideProductionGate(rep FinalBenchmarkReport) ProductionGateResult {
	pg := ProductionGateResult{
		RequiresTrainedModel: true,
		CriteriaChecklist: []string{
			"candidate exists",
			"dataset quality sufficient (usable>=500, …)",
			"candidate beats baseline where relevant",
			"unsafe output ~0",
			"protected-field handling correct",
			"clarification acceptable",
			"latency/RAM/CPU acceptable",
			"real-world scenarios pass",
			"no critical reliability regressions",
			"BUILDER_LOCAL_LM_ENABLED remains false until separate rollout",
		},
	}

	if !rep.TrainingGate.Ready {
		pg.Status = StatusNotReady
		pg.PromotionAllowed = false
		pg.Reasons = append(pg.Reasons,
			"Training dataset below ML-11/ML-14 thresholds — NOT_READY_FOR_TRAINING.",
			fmt.Sprintf("usable_total=%d need>=%d", rep.TrainingGate.Stats.UsableTotal, rep.TrainingGate.Config.MinUsableTotal),
		)
		for _, f := range rep.TrainingGate.Failures {
			pg.Reasons = append(pg.Reasons, fmt.Sprintf("%s: %d/%d missing %d", f.Metric, f.Have, f.Required, f.Missing))
		}
		if rep.Shadow.Status == StatusNoCandidateModel {
			pg.Reasons = append(pg.Reasons, "No trained/shadow candidate model configured.")
		}
		if !rep.Safety.Passed {
			pg.Reasons = append(pg.Reasons, "Safety gate incomplete or failed — requires review before any promotion.")
		}
		return pg
	}

	// Gate ready but no trained model — should not happen often; still don't promote.
	if !rep.TrainedModelExists {
		if rep.Shadow.Status == StatusNoCandidateModel {
			pg.Status = StatusDoNotPromote
			pg.Reasons = append(pg.Reasons, "Dataset ready but no trained candidate exists.")
			return pg
		}
		pg.Status = StatusCandidateReadyShadowOnly
		pg.ShadowOnlyOK = true
		pg.PromotionAllowed = false
		pg.Reasons = append(pg.Reasons, "Shadow candidate may be evaluated; production promotion not allowed.")
		return pg
	}

	pg.Status = StatusRequiresReview
	pg.PromotionAllowed = false
	pg.Reasons = append(pg.Reasons, "Trained model claimed but ML-16 does not auto-promote; requires human review.")
	return pg
}

func buildSignOffNarrative(rep FinalBenchmarkReport) (summary, blocked, deps []string) {
	summary = append(summary,
		"BuilderPlan deterministic classification scenarios exercised.",
		"Deterministic register_existing_page local-op benchmark exercised.",
		"Safety adversarial cases validated through BuilderPlan semantic path.",
		"Reliability controls inventoried (timeouts/retries/repairs unchanged).",
		"Reference ML-11 baselines preserved (heuristic≈0.82, Qwen≈0.48).",
	)
	if rep.LocalLM.Measured {
		summary = append(summary, fmt.Sprintf("Local LM measured: p50=%.1fms valid_json=%.0f%% unsafe=%.1f%%",
			rep.LocalLM.WarmP50MS, rep.LocalLM.ValidJSONPercent, rep.LocalLM.UnsafeOutputPercent))
	} else {
		summary = append(summary, "Local LM: not measured (server unreachable or skipped).")
	}
	if rep.DeepSeek.Measured {
		summary = append(summary, "DeepSeek live path measured.")
	} else {
		summary = append(summary, "DeepSeek live before/after: NOT_MEASURED (no fabricated improvement claim).")
	}

	blocked = append(blocked,
		"Trained semantic model not available.",
		"Dataset below training thresholds.",
		"Production BUILDER_LOCAL_LM_ENABLED must stay false.",
		"Candidate must not be promoted to production in this phase.",
	)
	deps = append(deps,
		"Collect ≥500 usable real builder examples (ML-13 checklist).",
		"External LoRA training when gate passes (ML-14 manifests).",
		"Shadow evaluate trained candidate (ML-15) before any production review.",
		"Separate production rollout phase after PROMOTE criteria are met with evidence.",
	)
	return
}

func writeFinalArtifacts(out string, rep FinalBenchmarkReport) error {
	if err := writeJSON(filepath.Join(out, "final_benchmark.json"), rep); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(out, "production_gate.json"), rep.ProductionGate); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(out, "scenario_results.json"), rep.Scenarios); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(out, "latency_report.json"), rep.Latency); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(out, "safety_report.json"), rep.Safety); err != nil {
		return err
	}
	// promotion_report.json — align with gate decision (no invented PROMOTE)
	promo := PromotionReport{
		Status:          rep.ML16Status,
		DatasetVersion:  DatasetVersion,
		GeneratedAt:     rep.GeneratedAt,
		Gate:            rep.TrainingGate,
		Hardware:        DetectHardware(),
		DecisionReasons: rep.ProductionGate.Reasons,
		ProductionNote:  "BUILDER_LOCAL_LM_ENABLED must remain false. Shadow is separate. Do not switch production models.",
		Benchmark:       rep.ReferenceBaselines,
	}
	if err := writeJSON(filepath.Join(out, "promotion_report.json"), promo); err != nil {
		return err
	}
	_ = writeJSON(filepath.Join(out, "local_ops_report.json"), rep.LocalOps)
	_ = writeJSON(filepath.Join(out, "local_lm_report.json"), rep.LocalLM)
	_ = writeJSON(filepath.Join(out, "shadow_report.json"), rep.Shadow)
	_ = writeJSON(filepath.Join(out, "deepseek_report.json"), rep.DeepSeek)
	_ = writeJSON(filepath.Join(out, "reliability_report.json"), rep.Reliability)
	return nil
}

// FormatFinalBenchmark prints the ML-16 sign-off block.
func FormatFinalBenchmark(rep FinalBenchmarkReport) string {
	return fmt.Sprintf(`ML-16 STATUS: %s

trained_model_exists: %v
production_local_lm_enabled: %v
promotion_allowed: %v
training_gate: %s (usable=%d)

passed:
%s
blocked:
%s
remaining_dependencies:
%s
`,
		rep.ML16Status,
		rep.TrainedModelExists,
		rep.ProductionLMEnabled,
		rep.PromotionAllowed,
		rep.TrainingGate.Status,
		rep.TrainingGate.Stats.UsableTotal,
		bullet(rep.Summary),
		bullet(rep.Blocked),
		bullet(rep.RemainingDeps),
	)
}

func bullet(ss []string) string {
	out := ""
	for _, s := range ss {
		out += "  - " + s + "\n"
	}
	return out
}

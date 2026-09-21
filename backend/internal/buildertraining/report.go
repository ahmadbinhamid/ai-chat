package buildertraining

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// BenchmarkRow is one row in the A/B comparison table.
type BenchmarkRow struct {
	Model                  string  `json:"model"`
	Accuracy               float64 `json:"accuracy"`
	ProtectedFieldAccuracy float64 `json:"protected_field_accuracy"`
	ClarificationAccuracy  float64 `json:"clarification_accuracy"`
	ValidJSONPercent       float64 `json:"valid_json_percent"`
	UnsafeOutputPercent    float64 `json:"unsafe_output_percent"`
	P50MS                  float64 `json:"p50_ms"`
	P95MS                  float64 `json:"p95_ms"`
	RAMMB                  float64 `json:"ram_mb,omitempty"`
	CPUPercent             float64 `json:"cpu_percent,omitempty"`
	Evaluated              bool    `json:"evaluated"`
	Notes                  string  `json:"notes,omitempty"`
}

// PromotionCriteria documents the promotion gate thresholds.
type PromotionCriteria struct {
	RequireBetterSemanticAccuracy bool    `json:"require_better_semantic_accuracy"`
	RequireSafetyEqualOrBetter    bool    `json:"require_safety_equal_or_better"`
	MaxInvalidJSONRate            float64 `json:"max_invalid_json_rate"`
	MaxP95MS                      float64 `json:"max_p95_ms"`
	MaxRAMMB                      float64 `json:"max_ram_mb"`
	NoClarificationRegression     bool    `json:"no_clarification_regression"`
	NoProtectedFieldRegression    bool    `json:"no_protected_field_regression"`
}

// DefaultPromotionCriteria returns conservative ML-11 thresholds.
func DefaultPromotionCriteria() PromotionCriteria {
	return PromotionCriteria{
		RequireBetterSemanticAccuracy: true,
		RequireSafetyEqualOrBetter:    true,
		MaxInvalidJSONRate:            0.05,
		MaxP95MS:                      5000,
		MaxRAMMB:                      2048,
		NoClarificationRegression:     true,
		NoProtectedFieldRegression:    true,
	}
}

// PromotionReport is the final ML-11 decision artifact.
type PromotionReport struct {
	Status             string             `json:"status"`
	DatasetVersion     string             `json:"dataset_version"`
	GeneratedAt        string             `json:"generated_at"`
	Gate               GateResult         `json:"gate"`
	Hardware           HardwareInfo       `json:"hardware"`
	Training           TrainAttemptResult `json:"training"`
	Criteria           PromotionCriteria  `json:"criteria"`
	Benchmark          []BenchmarkRow     `json:"benchmark"`
	HeuristicMetrics   *ModelMetrics      `json:"heuristic_metrics,omitempty"`
	BaseModelMetrics   *ModelMetrics      `json:"base_model_metrics,omitempty"`
	TrainedMetrics     *ModelMetrics      `json:"trained_metrics,omitempty"`
	AdversarialSummary map[string]float64 `json:"adversarial_summary,omitempty"`
	DecisionReasons    []string           `json:"decision_reasons"`
	ProductionNote     string             `json:"production_note"`
}

// DecidePromotion compares trained vs base when both exist; otherwise returns
// NOT_READY or DO_NOT_PROMOTE without declaring a fabricated winner.
func DecidePromotion(gate GateResult, train TrainAttemptResult, heuristic, base, trained *ModelMetrics) PromotionReport {
	hw := DetectHardware()
	rep := PromotionReport{
		DatasetVersion: DatasetVersion,
		GeneratedAt:    time.Now().UTC().Format(time.RFC3339),
		Gate:           gate,
		Hardware:       hw,
		Training:       train,
		Criteria:       DefaultPromotionCriteria(),
		ProductionNote: "BUILDER_LOCAL_LM_ENABLED must remain false for production until a separate rollout phase. Do not switch providers in this phase.",
	}
	crit := rep.Criteria

	if !gate.Ready {
		rep.Status = StatusNotReady
		rep.DecisionReasons = append(rep.DecisionReasons,
			"Dataset sufficiency gate failed — training skipped.",
			fmt.Sprintf("usable_total=%d need>=%d", gate.Stats.UsableTotal, gate.Config.MinUsableTotal),
		)
		for _, f := range gate.Failures {
			rep.DecisionReasons = append(rep.DecisionReasons,
				fmt.Sprintf("%s: have=%d required=%d missing=%d", f.Metric, f.Have, f.Required, f.Missing))
		}
		rep.Benchmark = buildBenchmark(heuristic, base, trained)
		return rep
	}
	if !train.Attempted {
		st := StatusFromTrainAttempt(train)
		rep.Status = st
		if st == StatusTrainingEnvUnsupported {
			rep.DecisionReasons = append(rep.DecisionReasons,
				"Training environment not supported for in-process LoRA.",
				train.SkippedReason,
				"External training manifests written; no weights fabricated.",
			)
		} else {
			rep.Status = StatusDoNotPromote
			rep.DecisionReasons = append(rep.DecisionReasons,
				"Training was not performed: "+train.SkippedReason,
				"No trained candidate to promote.",
			)
		}
		rep.Benchmark = buildBenchmark(heuristic, base, trained)
		return rep
	}
	if trained == nil || base == nil {
		rep.Status = StatusDoNotPromote
		rep.DecisionReasons = append(rep.DecisionReasons, "Missing trained or base metrics — cannot promote.")
		rep.Benchmark = buildBenchmark(heuristic, base, trained)
		return rep
	}

	ok := true
	if crit.RequireBetterSemanticAccuracy && !(trained.SemanticAccuracy > base.SemanticAccuracy) {
		ok = false
		rep.DecisionReasons = append(rep.DecisionReasons, "trained semantic accuracy is not better than base")
	}
	if crit.RequireSafetyEqualOrBetter && trained.UnsafeOutputRate > base.UnsafeOutputRate {
		ok = false
		rep.DecisionReasons = append(rep.DecisionReasons, "trained unsafe rate worse than base")
	}
	if trained.InvalidJSONRate > crit.MaxInvalidJSONRate {
		ok = false
		rep.DecisionReasons = append(rep.DecisionReasons, "invalid JSON rate above threshold")
	}
	if trained.Perf.P95MS > crit.MaxP95MS {
		ok = false
		rep.DecisionReasons = append(rep.DecisionReasons, "p95 latency above threshold")
	}
	if crit.NoClarificationRegression && trained.ClarificationAccuracy < base.ClarificationAccuracy {
		ok = false
		rep.DecisionReasons = append(rep.DecisionReasons, "clarification regression vs base")
	}
	if crit.NoProtectedFieldRegression && trained.ProtectedFieldAccuracy < base.ProtectedFieldAccuracy {
		ok = false
		rep.DecisionReasons = append(rep.DecisionReasons, "protected-field regression vs base")
	}
	if heuristic != nil && trained.ClarificationAccuracy < heuristic.ClarificationAccuracy-0.05 {
		ok = false
		rep.DecisionReasons = append(rep.DecisionReasons, "clarification regression vs heuristic")
	}

	if ok {
		rep.Status = StatusPromote
		rep.DecisionReasons = append(rep.DecisionReasons, "All promotion criteria met — still requires separate production rollout phase.")
	} else {
		rep.Status = StatusDoNotPromote
		if len(rep.DecisionReasons) == 0 {
			rep.DecisionReasons = append(rep.DecisionReasons, "Promotion criteria not met.")
		}
	}
	rep.Benchmark = buildBenchmark(heuristic, base, trained)
	rep.HeuristicMetrics = heuristic
	rep.BaseModelMetrics = base
	rep.TrainedMetrics = trained
	return rep
}

func buildBenchmark(heuristic, base, trained *ModelMetrics) []BenchmarkRow {
	row := func(m *ModelMetrics, label, notes string) BenchmarkRow {
		if m == nil {
			return BenchmarkRow{Model: label, Evaluated: false, Notes: notes}
		}
		return BenchmarkRow{
			Model:                  m.ModelName,
			Accuracy:               m.SemanticAccuracy,
			ProtectedFieldAccuracy: m.ProtectedFieldAccuracy,
			ClarificationAccuracy:  m.ClarificationAccuracy,
			ValidJSONPercent:       m.ValidJSONPercent,
			UnsafeOutputPercent:    100 * m.UnsafeOutputRate,
			P50MS:                  m.Perf.P50MS,
			P95MS:                  m.Perf.P95MS,
			RAMMB:                  m.Perf.RAMMB,
			CPUPercent:             m.Perf.CPUPercent,
			Evaluated:              true,
		}
	}
	return []BenchmarkRow{
		ReferenceBaselines[0],
		ReferenceBaselines[1],
		row(heuristic, "heuristic (this run)", "deterministic/heuristic baseline this run"),
		row(base, "qwen2.5-0.5b-base (this run)", "base model not evaluated (server down or skipped)"),
		row(trained, "trained-candidate", "no trained candidate"),
	}
}

// WriteGateArtifacts writes gate-only outputs (no training framework load beyond config docs).
func WriteGateArtifacts(outDir string, stats DatasetStats, gate GateResult, validation *DatasetValidation) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outDir, "dataset_stats.json"), stats); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outDir, "gate_report.json"), gate); err != nil {
		return err
	}
	if validation != nil {
		if err := writeJSON(filepath.Join(outDir, "dataset_validation.json"), validation); err != nil {
			return err
		}
	}
	// Document intended train config without starting training.
	full := BuildFullTrainingConfig(stats, "")
	_ = writeJSON(filepath.Join(outDir, "train_config.json"), full)
	empty := map[string]any{"status": "skipped", "reason": StatusNotReady}
	if gate.Ready {
		empty["reason"] = "gate passed — run train / evaluate separately"
	}
	_ = writeJSON(filepath.Join(outDir, "train_metrics.json"), empty)
	_ = writeJSON(filepath.Join(outDir, "validation_metrics.json"), empty)
	_ = writeJSON(filepath.Join(outDir, "test_metrics.json"), empty)
	return nil
}

// WriteArtifacts writes ML-14 JSON artifacts under outDir.
func WriteArtifacts(outDir string, stats DatasetStats, gate GateResult, train TrainAttemptResult, rep PromotionReport, trainM, valM, testM *ModelMetrics) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outDir, "dataset_stats.json"), stats); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outDir, "gate_report.json"), gate); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outDir, "train_attempt.json"), train); err != nil {
		return err
	}
	empty := map[string]any{"status": "skipped", "reason": "training not performed or split empty"}
	if trainM != nil {
		if err := writeJSON(filepath.Join(outDir, "train_metrics.json"), trainM); err != nil {
			return err
		}
	} else {
		_ = writeJSON(filepath.Join(outDir, "train_metrics.json"), empty)
	}
	if valM != nil {
		if err := writeJSON(filepath.Join(outDir, "validation_metrics.json"), valM); err != nil {
			return err
		}
	} else {
		_ = writeJSON(filepath.Join(outDir, "validation_metrics.json"), empty)
	}
	if testM != nil {
		if err := writeJSON(filepath.Join(outDir, "test_metrics.json"), testM); err != nil {
			return err
		}
	} else {
		_ = writeJSON(filepath.Join(outDir, "test_metrics.json"), empty)
	}
	if err := writeJSON(filepath.Join(outDir, "benchmark.json"), map[string]any{
		"dataset_version":     DatasetVersion,
		"reference_baselines": ReferenceBaselines,
		"rows":                rep.Benchmark,
		"adversarial":         rep.AdversarialSummary,
	}); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outDir, "promotion_report.json"), rep); err != nil {
		return err
	}
	return nil
}

// FormatSummary returns a human-readable CLI summary.
func FormatSummary(rep PromotionReport) string {
	b, _ := json.MarshalIndent(map[string]any{
		"status":               rep.Status,
		"usable_total":         rep.Gate.Stats.UsableTotal,
		"additional_needed":    rep.Gate.AdditionalNeeded,
		"training_attempted":   rep.Training.Attempted,
		"training_skipped":     rep.Training.SkippedReason,
		"hardware":             rep.Hardware,
		"benchmark":            rep.Benchmark,
		"decision_reasons":     rep.DecisionReasons,
		"production_note":      rep.ProductionNote,
	}, "", "  ")
	return string(b)
}

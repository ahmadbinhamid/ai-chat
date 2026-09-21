package buildertraining

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ai-chat/internal/builderdataset"
	"ai-chat/internal/builderintelligence/providers"
)

// PipelineOptions configures gate/train/evaluate/report commands.
type PipelineOptions struct {
	DatasetDir  string
	ArtifactDir string
	LlamaURL    string
	EvalBaseLM  bool
	FromDB      bool   // load live builder_execution_examples (dev/staging CLI)
	TenantID    uint64 // 0 = all tenants when FromDB (operator CLI counts/transform only)
}

func artifactDir(opts PipelineOptions) string {
	if opts.ArtifactDir != "" {
		return opts.ArtifactDir
	}
	return filepath.Join("artifacts", DatasetVersion)
}

func loadStats(opts PipelineOptions) (stats DatasetStats, train, val, test []builderdataset.TrainingExample, validation DatasetValidation, present bool) {
	stats = DatasetStats{DatasetVersion: DatasetVersion, IntentDistribution: map[string]int{}}

	if opts.FromDB {
		examples, err := loadExamplesFromDB(opts.TenantID)
		if err != nil {
			return stats, nil, nil, nil, DatasetValidation{OK: false, Errors: []string{err.Error()}}, false
		}
		result := builderdataset.Transform(examples)
		stats = ComputeStats(result.Train, result.Validation, result.Test)
		validation = ValidateSplits(result.Train, result.Validation, result.Test)
		return stats, result.Train, result.Validation, result.Test, validation, true
	}

	datasetDir := opts.DatasetDir
	if datasetDir == "" {
		if st, err := os.Stat(DatasetVersion); err == nil && st.IsDir() {
			datasetDir = DatasetVersion
		}
	}
	if datasetDir == "" {
		return stats, nil, nil, nil, DatasetValidation{OK: false, Errors: []string{"no dataset dir"}}, false
	}
	st, err := os.Stat(datasetDir)
	if err != nil || !st.IsDir() {
		return stats, nil, nil, nil, DatasetValidation{OK: false, Errors: []string{"dataset dir missing"}}, false
	}
	train, val, test, err = LoadSplitDir(datasetDir)
	if err != nil {
		return stats, nil, nil, nil, DatasetValidation{OK: false, Errors: []string{err.Error()}}, false
	}
	stats = ComputeStats(train, val, test)
	validation = ValidateSplits(train, val, test)
	return stats, train, val, test, validation, true
}

// RunGate: readiness only — no training framework load beyond writing config docs.
func RunGate(opts PipelineOptions) (GateResult, error) {
	out := artifactDir(opts)
	stats, _, _, _, validation, _ := loadStats(opts)
	gate := EvaluateGate(stats, DefaultGate())
	if !validation.OK && stats.UsableTotal > 0 {
		gate.Failures = append(gate.Failures, GateFailure{
			Metric: "dataset_validation", Have: 0, Required: 1, Missing: 1,
		})
		if gate.AdditionalNeeded == nil {
			gate.AdditionalNeeded = map[string]int{}
		}
		gate.AdditionalNeeded["dataset_validation"] = 1
		gate.Ready = false
		gate.Status = StatusNotReady
		gate.Recommendation = "Fix dataset validation errors before training: " + strings.Join(validation.Errors, "; ")
	}
	if err := WriteGateArtifacts(out, stats, gate, &validation); err != nil {
		return gate, err
	}
	return gate, nil
}

// RunTrain: gate → validate → train (or TRAINING_ENVIRONMENT_NOT_SUPPORTED).
func RunTrain(opts PipelineOptions) (PromotionReport, error) {
	out := artifactDir(opts)
	stats, train, val, test, validation, present := loadStats(opts)
	gate := EvaluateGate(stats, DefaultGate())
	if present && !validation.OK {
		gate.Ready = false
		gate.Status = StatusNotReady
		gate.Recommendation = "Dataset validation failed: " + strings.Join(validation.Errors, "; ")
	}

	// Gate fail: stop before any training attempt / weight creation.
	if !gate.Ready {
		_ = WriteGateArtifacts(out, stats, gate, &validation)
		trainAttempt := TrainAttemptResult{
			Status:        StatusNotReady,
			SkippedReason: StatusNotReady + ": " + gate.Recommendation,
			Hardware:      DetectHardware(),
			Config:        DefaultTrainingConfig(stats, out),
		}
		rep := DecidePromotion(gate, trainAttempt, nil, nil, nil)
		_ = writeJSON(filepath.Join(out, "promotion_report.json"), rep)
		_ = writeJSON(filepath.Join(out, "train_attempt.json"), trainAttempt)
		return rep, nil
	}

	_ = writeJSON(filepath.Join(out, "dataset_validation.json"), validation)
	trainAttempt := AttemptTrain(gate, stats, out)
	rep := DecidePromotion(gate, trainAttempt, nil, nil, nil)
	_ = train
	_ = val
	_ = test
	if err := WriteArtifacts(out, stats, gate, trainAttempt, rep, nil, nil, nil); err != nil {
		return rep, err
	}
	return rep, nil
}

// RunEvaluate compares heuristic / base / trained (if present) on the same held-out set.
func RunEvaluate(opts PipelineOptions) (PromotionReport, error) {
	out := artifactDir(opts)
	stats, _, _, test, validation, present := loadStats(opts)
	gate := EvaluateGate(stats, DefaultGate())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var cases []EvalCase
	if present && len(test) > 0 {
		cases = CasesFromTrainingExamples(test)
	} else {
		cases = append(BuilderStyleEvalCases(), AdversarialCases()...)
	}

	heuristicM, _ := RunProviderEval(ctx, "heuristic", providers.Heuristic{}, cases)
	var baseM *ModelMetrics
	if opts.EvalBaseLM && opts.LlamaURL != "" && pingLocalLM(opts.LlamaURL) {
		m, _ := RunProviderEval(ctx, "qwen2.5-0.5b-base", providers.HTTP{
			BaseURL: opts.LlamaURL, Model: "qwen2.5-0.5b-instruct", Timeout: 8 * time.Second,
		}, cases)
		baseM = &m
	}

	advSummary := map[string]float64{}
	advH, _ := RunProviderEval(ctx, "heuristic_adversarial", providers.Heuristic{}, AdversarialCases())
	advSummary["heuristic_unsafe_rate"] = advH.UnsafeOutputRate
	advSummary["heuristic_valid_json"] = advH.ValidJSONPercent
	if baseM != nil {
		advB, _ := RunProviderEval(ctx, "base_adversarial", providers.HTTP{
			BaseURL: opts.LlamaURL, Model: "qwen2.5-0.5b-instruct", Timeout: 8 * time.Second,
		}, AdversarialCases())
		advSummary["base_unsafe_rate"] = advB.UnsafeOutputRate
		advSummary["base_valid_json"] = advB.ValidJSONPercent
	}

	trainAttempt := TrainAttemptResult{
		Status:        StatusDoNotPromote,
		SkippedReason: "evaluate-only; training not invoked",
		Hardware:      DetectHardware(),
		Config:        DefaultTrainingConfig(stats, out),
	}
	if !gate.Ready {
		trainAttempt.Status = StatusNotReady
		trainAttempt.SkippedReason = StatusNotReady
	}

	var trainedM *ModelMetrics // never fabricated
	rep := DecidePromotion(gate, trainAttempt, &heuristicM, baseM, trainedM)
	rep.AdversarialSummary = advSummary
	rep.HeuristicMetrics = &heuristicM
	rep.BaseModelMetrics = baseM
	if !present || stats.UsableTotal == 0 {
		for i := range rep.Benchmark {
			if strings.Contains(rep.Benchmark[i].Model, "this run") && rep.Benchmark[i].Notes == "" {
				rep.Benchmark[i].Notes = "evaluated on labeled eval-only set (not production training data)"
			}
		}
	}

	var testMetrics *ModelMetrics
	if present && len(test) > 0 {
		testMetrics = &heuristicM
	}
	_ = validation
	if err := WriteArtifacts(out, stats, gate, trainAttempt, rep, nil, nil, testMetrics); err != nil {
		return rep, err
	}
	return rep, nil
}

// RunReport regenerates the promotion/benchmark report (evaluate + decide).
func RunReport(opts PipelineOptions) (PromotionReport, error) {
	return RunEvaluate(opts)
}

// RunPipeline keeps backward compatibility: gate → (no train if fail) → evaluate → report.
func RunPipeline(opts PipelineOptions) (PromotionReport, error) {
	gate, err := RunGate(opts)
	if err != nil {
		return PromotionReport{}, err
	}
	if !gate.Ready {
		trainAttempt := TrainAttemptResult{
			Status: StatusNotReady, SkippedReason: StatusNotReady + ": " + gate.Recommendation,
			Hardware: DetectHardware(), Config: DefaultTrainingConfig(gate.Stats, artifactDir(opts)),
		}
		rep := DecidePromotion(gate, trainAttempt, nil, nil, nil)
		_ = writeJSON(filepath.Join(artifactDir(opts), "promotion_report.json"), rep)
		return rep, nil
	}
	// Gate passed: attempt train (likely env unsupported), then evaluate.
	rep, err := RunTrain(opts)
	if err != nil {
		return rep, err
	}
	evalRep, err := RunEvaluate(opts)
	if err != nil {
		return evalRep, err
	}
	// Prefer evaluate metrics but keep train status if env unsupported.
	if rep.Status == StatusTrainingEnvUnsupported {
		evalRep.Status = StatusTrainingEnvUnsupported
		evalRep.Training = rep.Training
		evalRep.DecisionReasons = append(rep.DecisionReasons, evalRep.DecisionReasons...)
		_ = writeJSON(filepath.Join(artifactDir(opts), "promotion_report.json"), evalRep)
	}
	return evalRep, nil
}

func pingLocalLM(baseURL string) bool {
	client := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := client.Get(strings.TrimRight(baseURL, "/") + "/models")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 500
}

// FormatGateText prints the human gate summary.
func FormatGateText(gate GateResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "STATUS=%s\n", gate.Status)
	fmt.Fprintf(&b, "usable_total=%d need>=%d\n", gate.Stats.UsableTotal, gate.Config.MinUsableTotal)
	for _, f := range gate.Failures {
		fmt.Fprintf(&b, "%s: %d / %d (missing %d)\n", strings.ToUpper(f.Metric), f.Have, f.Required, f.Missing)
	}
	if len(gate.AdditionalNeeded) > 0 {
		fmt.Fprintf(&b, "additional_needed=%v\n", gate.AdditionalNeeded)
	}
	fmt.Fprintf(&b, "recommendation: %s\n", gate.Recommendation)
	return b.String()
}

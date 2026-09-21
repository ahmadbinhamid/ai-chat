package buildertraining

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunFinalBenchmark_NotReady(t *testing.T) {
	dir := t.TempDir()
	rep, err := RunFinalBenchmark(FinalBenchmarkOptions{
		ArtifactDir: dir,
		EvalLocalLM: false,
		FromDB:      false,
		DatasetDir:  filepath.Join(dir, "missing-dataset"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.ML16Status != StatusNotReady {
		t.Fatalf("status=%s", rep.ML16Status)
	}
	if rep.PromotionAllowed {
		t.Fatal("promotion must be false")
	}
	if rep.TrainedModelExists {
		t.Fatal("no trained model")
	}
	if rep.Shadow.Status != StatusNoCandidateModel {
		t.Fatalf("shadow=%s", rep.Shadow.Status)
	}
	if rep.DeepSeek.ImprovementClaimed {
		t.Fatal("must not claim DeepSeek improvement")
	}
	for _, name := range []string{
		"final_benchmark.json", "production_gate.json", "scenario_results.json",
		"latency_report.json", "safety_report.json", "promotion_report.json",
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	passed := 0
	for _, s := range rep.Scenarios {
		if s.Passed {
			passed++
		}
	}
	if passed == 0 {
		t.Fatal("expected some scenarios to pass")
	}
}

func TestDecideProductionGate_NotReady(t *testing.T) {
	rep := FinalBenchmarkReport{
		TrainingGate: GateResult{Ready: false, Status: StatusNotReady, Stats: DatasetStats{UsableTotal: 2}, Config: DefaultGate()},
		Shadow:       ShadowReport{Status: StatusNoCandidateModel},
		Safety:       SafetyReport{Passed: true},
	}
	pg := decideProductionGate(rep)
	if pg.Status != StatusNotReady || pg.PromotionAllowed {
		t.Fatalf("%+v", pg)
	}
}

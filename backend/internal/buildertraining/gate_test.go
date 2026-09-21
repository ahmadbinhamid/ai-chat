package buildertraining

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ai-chat/internal/builderdataset"
	"ai-chat/internal/builderintelligence/providers"
)

func TestGate_EmptyDataset_NotReady(t *testing.T) {
	stats := DatasetStats{DatasetVersion: DatasetVersion, IntentDistribution: map[string]int{}}
	gate := EvaluateGate(stats, DefaultGate())
	if gate.Ready {
		t.Fatal("expected not ready")
	}
	if gate.Status != StatusNotReady {
		t.Fatalf("status=%s", gate.Status)
	}
	if gate.AdditionalNeeded["usable_total"] < 500 {
		t.Fatalf("expected usable_total missing >=500, got %v", gate.AdditionalNeeded)
	}
}

func TestGate_Sufficient(t *testing.T) {
	stats := DatasetStats{
		DatasetVersion:       DatasetVersion,
		UsableTotal:          500,
		PositiveSemantic:     200,
		DistinctSemanticKeys: 100,
		WithProtectedFields:  50,
		WithPreferences:      50,
		WithClarification:    30,
		SEORelated:           40,
		Compound:             40,
		IntentDistribution:   map[string]int{},
	}
	gate := EvaluateGate(stats, DefaultGate())
	if !gate.Ready {
		t.Fatalf("expected ready, fails=%v", gate.Failures)
	}
}

func TestAttemptTrain_SkipsWhenNotReady(t *testing.T) {
	stats := DatasetStats{DatasetVersion: DatasetVersion, IntentDistribution: map[string]int{}}
	gate := EvaluateGate(stats, DefaultGate())
	res := AttemptTrain(gate, stats, t.TempDir())
	if res.Attempted {
		t.Fatal("must not attempt train")
	}
	if res.SkippedReason == "" {
		t.Fatal("expected skip reason")
	}
}

func TestScorePredictions_Exact(t *testing.T) {
	gold := []builderdataset.TrainingExample{{
		Target: builderdataset.TrainingTarget{
			Constraints:        []string{"preserve current pricing"},
			ProtectedFields:    []string{"pricing"},
			NeedsClarification: false,
		},
	}}
	preds := []Prediction{{
		Refinement: providers.SemanticRefinement{
			Constraints:     []string{"preserve current pricing"},
			ProtectedFields: []string{"pricing"},
		},
	}}
	m := ScorePredictions("test", gold, preds, PerfMetrics{})
	if m.ExactStructuredOutputRate != 1 {
		t.Fatalf("exact=%v", m.ExactStructuredOutputRate)
	}
	if m.ProtectedFields.F1 != 1 {
		t.Fatalf("pf f1=%v", m.ProtectedFields.F1)
	}
}

func TestHeuristicEval_AdversarialSafe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m, preds := RunProviderEval(ctx, "heuristic", providers.Heuristic{}, AdversarialCases())
	if m.ExamplesEvaluated != len(AdversarialCases()) {
		t.Fatalf("n=%d", m.ExamplesEvaluated)
	}
	for i, p := range preds {
		if p.Unsafe {
			t.Fatalf("case %d unsafe: %+v", i, p.Refinement)
		}
	}
}

func TestDecidePromotion_NotReady(t *testing.T) {
	stats := DatasetStats{DatasetVersion: DatasetVersion, IntentDistribution: map[string]int{}}
	gate := EvaluateGate(stats, DefaultGate())
	train := AttemptTrain(gate, stats, t.TempDir())
	rep := DecidePromotion(gate, train, nil, nil, nil)
	if rep.Status != StatusNotReady {
		t.Fatalf("status=%s", rep.Status)
	}
}

func TestWriteArtifacts(t *testing.T) {
	dir := t.TempDir()
	stats := DatasetStats{DatasetVersion: DatasetVersion, IntentDistribution: map[string]int{}}
	gate := EvaluateGate(stats, DefaultGate())
	train := AttemptTrain(gate, stats, dir)
	rep := DecidePromotion(gate, train, nil, nil, nil)
	if err := WriteArtifacts(dir, stats, gate, train, rep, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"dataset_stats.json", "benchmark.json", "promotion_report.json",
		"train_metrics.json", "validation_metrics.json", "test_metrics.json",
	} {
		p := filepath.Join(dir, name)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestComputeStats_FromExamples(t *testing.T) {
	ex := []builderdataset.TrainingExample{
		{
			Label:       builderdataset.QualityPositiveSemantic,
			SemanticKey: "a",
			Input:       builderdataset.TrainingInput{Intent: "seo_meta", Operations: []string{"update_seo_meta"}},
			Target:      builderdataset.TrainingTarget{ProtectedFields: []string{"meta_title"}},
			Metadata:    builderdataset.TrainingMetadata{Compound: true},
		},
		{
			Label:       builderdataset.QualityAmbiguousEval,
			SemanticKey: "b",
			Input:       builderdataset.TrainingInput{Intent: "ambiguous"},
			Target:      builderdataset.TrainingTarget{NeedsClarification: true, Preferences: []string{"modern"}},
		},
	}
	st := ComputeStats(ex, nil, nil)
	if st.UsableTotal != 2 || st.PositiveSemantic != 1 || st.DistinctSemanticKeys != 2 {
		t.Fatalf("%+v", st)
	}
	if st.WithProtectedFields != 1 || st.SEORelated != 1 || st.Compound != 1 {
		t.Fatalf("%+v", st)
	}
}

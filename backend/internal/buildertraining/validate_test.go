package buildertraining

import (
	"testing"

	"ai-chat/internal/builderdataset"
)

func TestValidateSplits_OK(t *testing.T) {
	train := []builderdataset.TrainingExample{{
		DatasetVersion: DatasetVersion, SemanticKey: "a", Label: builderdataset.QualityPositiveSemantic,
		Input: builderdataset.TrainingInput{Intent: "simple_edit"},
	}}
	val := []builderdataset.TrainingExample{{
		DatasetVersion: DatasetVersion, SemanticKey: "b", Label: builderdataset.QualityPositiveRouting,
		Input: builderdataset.TrainingInput{Intent: "seo_meta"},
	}}
	v := ValidateSplits(train, val, nil)
	if !v.OK {
		t.Fatalf("%+v", v)
	}
}

func TestValidateSplits_Leakage(t *testing.T) {
	train := []builderdataset.TrainingExample{{
		DatasetVersion: DatasetVersion, SemanticKey: "same", Label: builderdataset.QualityPositiveSemantic,
		Input: builderdataset.TrainingInput{Intent: "simple_edit"},
	}}
	test := []builderdataset.TrainingExample{{
		DatasetVersion: DatasetVersion, SemanticKey: "same", Label: builderdataset.QualityPositiveSemantic,
		Input: builderdataset.TrainingInput{Intent: "simple_edit"},
	}}
	v := ValidateSplits(train, nil, test)
	if v.OK || !v.SemanticLeakage {
		t.Fatalf("expected leakage: %+v", v)
	}
}

func TestValidateSplits_Sensitive(t *testing.T) {
	train := []builderdataset.TrainingExample{{
		DatasetVersion: DatasetVersion, SemanticKey: "s", Label: builderdataset.QualityPositiveSemantic,
		Input:  builderdataset.TrainingInput{Intent: "simple_edit", Prompt: "use sk-abcdefghijklmnop please"},
		Target: builderdataset.TrainingTarget{Constraints: []string{"x"}},
	}}
	v := ValidateSplits(train, nil, nil)
	if v.OK || v.SensitiveRows == 0 {
		t.Fatalf("expected sensitive: %+v", v)
	}
}

func TestStatusFromTrainAttempt_Env(t *testing.T) {
	st := StatusFromTrainAttempt(TrainAttemptResult{
		Status: StatusTrainingEnvUnsupported,
		SkippedReason: StatusTrainingEnvUnsupported + ": cpu",
	})
	if st != StatusTrainingEnvUnsupported {
		t.Fatalf("%s", st)
	}
}

func TestBuildFullTrainingConfig(t *testing.T) {
	cfg := BuildFullTrainingConfig(DatasetStats{DatasetHash: "abc", SplitHash: "def"}, "/tmp/out")
	if cfg.BaseModelID == "" || !cfg.DoNotTrainQuantized || cfg.LoRARank != 8 {
		t.Fatalf("%+v", cfg)
	}
	if cfg.TargetSchema == "" || cfg.MaxSeqLength != 512 {
		t.Fatalf("%+v", cfg)
	}
}

package buildertraining

import (
	"fmt"
	"strings"

	"ai-chat/internal/builderdataset"
)

// DatasetValidation is the pre-train integrity check (ML-14).
type DatasetValidation struct {
	OK                 bool     `json:"ok"`
	DatasetVersion     string   `json:"dataset_version"`
	DatasetHash        string   `json:"dataset_hash"`
	SplitHash          string   `json:"split_hash"`
	TrainCount         int      `json:"train_count"`
	ValidationCount    int      `json:"validation_count"`
	TestCount          int      `json:"test_count"`
	SemanticLeakage    bool     `json:"semantic_group_leakage"`
	LeakageKeysSample  []string `json:"leakage_keys_sample,omitempty"`
	InvalidJSONLRows   int      `json:"invalid_jsonl_rows"`
	MissingTargetRows  int      `json:"missing_required_target_rows"`
	SensitiveRows      int      `json:"sensitive_rows"`
	Errors             []string `json:"errors,omitempty"`
	Warnings           []string `json:"warnings,omitempty"`
}

// ValidateSplits checks ML-10 outputs before any training attempt.
// Does not regenerate splits.
func ValidateSplits(train, val, test []builderdataset.TrainingExample) DatasetValidation {
	v := DatasetValidation{
		DatasetVersion:  DatasetVersion,
		TrainCount:      len(train),
		ValidationCount: len(val),
		TestCount:       len(test),
		DatasetHash:     hashExamples(append(append(append([]builderdataset.TrainingExample{}, train...), val...), test...)),
		SplitHash:       hashSplits(train, val, test),
		OK:              true,
	}

	if len(train)+len(val)+len(test) == 0 {
		v.OK = false
		v.Errors = append(v.Errors, "empty dataset splits")
		return v
	}

	// Semantic-group leakage across train vs val/test.
	trainKeys := map[string]bool{}
	for _, ex := range train {
		if ex.SemanticKey != "" {
			trainKeys[ex.SemanticKey] = true
		}
	}
	checkLeak := func(split string, rows []builderdataset.TrainingExample) {
		for _, ex := range rows {
			if ex.SemanticKey == "" {
				continue
			}
			if trainKeys[ex.SemanticKey] {
				v.SemanticLeakage = true
				if len(v.LeakageKeysSample) < 8 {
					v.LeakageKeysSample = append(v.LeakageKeysSample, split+":"+ex.SemanticKey)
				}
			}
		}
	}
	checkLeak("validation", val)
	checkLeak("test", test)
	if v.SemanticLeakage {
		v.OK = false
		v.Errors = append(v.Errors, "semantic-group leakage between train and validation/test")
	}

	all := append(append(append([]builderdataset.TrainingExample{}, train...), val...), test...)
	for i, ex := range all {
		if ex.DatasetVersion != "" && ex.DatasetVersion != DatasetVersion {
			v.Warnings = append(v.Warnings, fmt.Sprintf("row %d dataset_version=%q want %q", i, ex.DatasetVersion, DatasetVersion))
		}
		if ex.Input.Intent == "" {
			v.MissingTargetRows++
			v.OK = false
		}
		if ex.Label == "" {
			v.MissingTargetRows++
			v.OK = false
		}
		// Target object must be present structurally (clarification may be empty).
		// Sensitive residual checks on any serialized-looking fields.
		if looksSensitive(ex) {
			v.SensitiveRows++
			v.OK = false
		}
	}
	if v.MissingTargetRows > 0 {
		v.Errors = append(v.Errors, fmt.Sprintf("%d rows missing required fields", v.MissingTargetRows))
	}
	if v.SensitiveRows > 0 {
		v.Errors = append(v.Errors, fmt.Sprintf("%d rows contain sensitive residual patterns", v.SensitiveRows))
	}
	return v
}

func looksSensitive(ex builderdataset.TrainingExample) bool {
	parts := []string{
		ex.Input.Prompt,
		strings.Join(ex.Target.Constraints, " "),
		strings.Join(ex.Target.Preferences, " "),
		strings.Join(ex.Target.ProtectedFields, " "),
	}
	blob := strings.ToLower(strings.Join(parts, " "))
	needles := []string{
		"sk-", "bearer ", "api_key", "api-key", "password=", "authorization:",
		"-----begin", "aws_secret", "private_key",
	}
	for _, n := range needles {
		if strings.Contains(blob, n) {
			return true
		}
	}
	return false
}

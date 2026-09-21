package builderdataset

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Snapshot is a deterministic, secret-free dataset summary artifact.
type Snapshot struct {
	DatasetVersion     string            `json:"dataset_version"`
	CreatedAt          string            `json:"created_at"`
	SourceCount        int               `json:"source_count"`
	UsableCount        int               `json:"usable_count"`
	ExcludedCount      int               `json:"excluded_count"`
	TrainCount         int               `json:"train_count"`
	ValidationCount    int               `json:"validation_count"`
	TestCount          int               `json:"test_count"`
	PositiveSemantic   int               `json:"positive_semantic"`
	PositiveRouting    int               `json:"positive_routing"`
	NegativeEval       int               `json:"negative_eval"`
	AmbiguousEval      int               `json:"ambiguous_eval"`
	DuplicateCollapsed int               `json:"duplicate_collapsed"`
	CategoryDistribution map[string]int  `json:"category_distribution"`
	IntentDistribution map[string]int    `json:"intent_distribution"`
	SourceDistribution map[string]int    `json:"source_distribution"`
	DatasetHash        string            `json:"dataset_hash"`
	SplitHash          string            `json:"split_hash,omitempty"`
	GateStatus         string            `json:"gate_status"`
	GateReady          bool              `json:"gate_ready"`
	MissingCounts      map[string]int    `json:"missing_counts,omitempty"`
	Underrepresented   []string          `json:"underrepresented,omitempty"`
	Duplicates         DuplicateStats    `json:"duplicates"`
}

// BuildSnapshot creates a snapshot from a readiness report + transform result.
func BuildSnapshot(result TransformResult, readiness ReadinessReport) Snapshot {
	cat := map[string]int{
		"positive_semantic": readiness.PositiveSemantic,
		"positive_routing":  readiness.PositiveRouting,
		"negative_eval":     readiness.NegativeEval,
		"ambiguous_eval":    readiness.AmbiguousEval,
	}
	return Snapshot{
		DatasetVersion:       DatasetVersion,
		CreatedAt:            time.Now().UTC().Format(time.RFC3339),
		SourceCount:          result.Report.TotalSource,
		UsableCount:          result.Report.Usable,
		ExcludedCount:        result.Report.Excluded,
		TrainCount:           result.Report.TrainCount,
		ValidationCount:      result.Report.ValidationCount,
		TestCount:            result.Report.TestCount,
		PositiveSemantic:     result.Report.PositiveSemantic,
		PositiveRouting:      result.Report.PositiveRouting,
		NegativeEval:         result.Report.NegativeEval,
		AmbiguousEval:        result.Report.AmbiguousEval,
		DuplicateCollapsed:   result.Report.DuplicateCollapsed,
		CategoryDistribution: cat,
		IntentDistribution:   copyIntMap(result.Report.IntentDistribution),
		SourceDistribution:   copyIntMap(result.Report.SourceDistribution),
		DatasetHash:          readiness.DatasetHash,
		GateStatus:           readiness.GateStatus,
		GateReady:            readiness.GateReady,
		MissingCounts:        readiness.MissingCounts,
		Underrepresented:     append([]string(nil), readiness.Underrepresented...),
		Duplicates:           readiness.Duplicates,
	}
}

func copyIntMap(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// WriteSnapshotJSON writes snapshot.json (and optional readiness.json) under dir.
func WriteSnapshotJSON(dir string, snap Snapshot, readiness *ReadinessReport) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	if readiness != nil {
		rb, err := json.MarshalIndent(readiness, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "readiness.json"), append(rb, '\n'), 0o644); err != nil {
			return err
		}
	}
	return nil
}

package buildertraining

import (
	"ai-chat/internal/builderdataset"
)

// LoadDatasetOrEmpty loads split JSONL when present. Missing dirs yield empty
// stats — never fabricates examples.
func LoadDatasetOrEmpty(dir string) (train, val, test []builderdataset.TrainingExample, stats DatasetStats, ok bool) {
	stats = DatasetStats{DatasetVersion: DatasetVersion, IntentDistribution: map[string]int{}}
	if dir == "" {
		return nil, nil, nil, stats, false
	}
	tr, v, te, err := LoadSplitDir(dir)
	if err != nil {
		return nil, nil, nil, stats, false
	}
	return tr, v, te, ComputeStats(tr, v, te), true
}

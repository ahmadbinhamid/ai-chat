package buildertraining

import "ai-chat/internal/builderdataset"

// GateConfig is the minimum dataset size before training may start.
type GateConfig struct {
	MinUsableTotal       int `json:"min_usable_total"`
	MinPositiveSemantic  int `json:"min_positive_semantic"`
	MinDistinctSemantic  int `json:"min_distinct_semantic_groups"`
	MinWithProtected     int `json:"min_with_protected_fields"`
	MinWithPreferences   int `json:"min_with_preferences"`
	MinClarification     int `json:"min_clarification"`
	MinSEORelated        int `json:"min_seo_related"`
	MinCompound          int `json:"min_compound"`
}

// DefaultGate returns the ML-11 recommended thresholds.
func DefaultGate() GateConfig {
	return GateConfig{
		MinUsableTotal:      500,
		MinPositiveSemantic: 200,
		MinDistinctSemantic: 100,
		MinWithProtected:    50,
		MinWithPreferences:  50,
		MinClarification:    30,
		MinSEORelated:       40,
		MinCompound:         40,
	}
}

// DatasetStats summarizes a loaded builder-semantic dataset.
type DatasetStats struct {
	DatasetVersion       string         `json:"dataset_version"`
	TrainCount           int            `json:"train_count"`
	ValidationCount      int            `json:"validation_count"`
	TestCount            int            `json:"test_count"`
	UsableTotal          int            `json:"usable_total"`
	PositiveSemantic     int            `json:"positive_semantic"`
	PositiveRouting      int            `json:"positive_routing"`
	NegativeEval         int            `json:"negative_eval"`
	AmbiguousEval        int            `json:"ambiguous_eval"`
	DistinctSemanticKeys int            `json:"distinct_semantic_groups"`
	WithProtectedFields  int            `json:"with_protected_fields"`
	WithPreferences      int            `json:"with_preferences"`
	WithClarification    int            `json:"with_clarification"`
	SEORelated           int            `json:"seo_related"`
	Compound             int            `json:"compound"`
	IntentDistribution   map[string]int `json:"intent_distribution"`
	DatasetHash          string         `json:"dataset_hash"`
	SplitHash            string         `json:"split_hash"`
}

// GateResult is the sufficiency decision.
type GateResult struct {
	Ready              bool              `json:"ready"`
	Status             string            `json:"status"`
	Config             GateConfig        `json:"config"`
	Stats              DatasetStats      `json:"stats"`
	Failures           []GateFailure     `json:"failures,omitempty"`
	Recommendation     string            `json:"recommendation"`
	AdditionalNeeded   map[string]int    `json:"additional_examples_needed,omitempty"`
}

// GateFailure names one unmet threshold.
type GateFailure struct {
	Metric   string `json:"metric"`
	Have     int    `json:"have"`
	Required int    `json:"required"`
	Missing  int    `json:"missing"`
}

// ComputeStats aggregates split JSONL rows.
func ComputeStats(train, val, test []builderdataset.TrainingExample) DatasetStats {
	all := append(append(append([]builderdataset.TrainingExample{}, train...), val...), test...)
	st := DatasetStats{
		DatasetVersion:     DatasetVersion,
		TrainCount:         len(train),
		ValidationCount:    len(val),
		TestCount:          len(test),
		UsableTotal:        len(all),
		IntentDistribution: map[string]int{},
	}
	keys := map[string]bool{}
	for _, ex := range all {
		keys[ex.SemanticKey] = true
		st.IntentDistribution[ex.Input.Intent]++
		switch ex.Label {
		case builderdataset.QualityPositiveSemantic:
			st.PositiveSemantic++
		case builderdataset.QualityPositiveRouting:
			st.PositiveRouting++
		case builderdataset.QualityNegativeEval:
			st.NegativeEval++
		case builderdataset.QualityAmbiguousEval:
			st.AmbiguousEval++
		}
		if len(ex.Target.ProtectedFields) > 0 {
			st.WithProtectedFields++
		}
		if len(ex.Target.Preferences) > 0 {
			st.WithPreferences++
		}
		if ex.Target.NeedsClarification || ex.Label == builderdataset.QualityAmbiguousEval {
			st.WithClarification++
		}
		if isSEORelated(ex) {
			st.SEORelated++
		}
		if ex.Metadata.Compound || ex.Input.Intent == "compound" {
			st.Compound++
		}
	}
	st.DistinctSemanticKeys = len(keys)
	st.DatasetHash = hashExamples(all)
	st.SplitHash = hashSplits(train, val, test)
	return st
}

func isSEORelated(ex builderdataset.TrainingExample) bool {
	if ex.Input.Intent == "seo_meta" {
		return true
	}
	for _, op := range ex.Input.Operations {
		if op == "update_seo_meta" {
			return true
		}
	}
	for _, f := range ex.Target.ProtectedFields {
		switch f {
		case "meta_title", "meta_description", "seo_keywords", "og_title", "og_description":
			return true
		}
	}
	return false
}

// EvaluateGate checks dataset sufficiency. Never invents examples.
func EvaluateGate(stats DatasetStats, cfg GateConfig) GateResult {
	if cfg.MinUsableTotal == 0 {
		cfg = DefaultGate()
	}
	var fails []GateFailure
	need := map[string]int{}
	check := func(metric string, have, req int) {
		if have < req {
			miss := req - have
			fails = append(fails, GateFailure{Metric: metric, Have: have, Required: req, Missing: miss})
			need[metric] = miss
		}
	}
	check("usable_total", stats.UsableTotal, cfg.MinUsableTotal)
	check("positive_semantic", stats.PositiveSemantic, cfg.MinPositiveSemantic)
	check("distinct_semantic_groups", stats.DistinctSemanticKeys, cfg.MinDistinctSemantic)
	check("with_protected_fields", stats.WithProtectedFields, cfg.MinWithProtected)
	check("with_preferences", stats.WithPreferences, cfg.MinWithPreferences)
	check("with_clarification", stats.WithClarification, cfg.MinClarification)
	check("seo_related", stats.SEORelated, cfg.MinSEORelated)
	check("compound", stats.Compound, cfg.MinCompound)

	res := GateResult{
		Config:           cfg,
		Stats:            stats,
		Failures:         fails,
		AdditionalNeeded: need,
	}
	if len(fails) == 0 {
		res.Ready = true
		res.Status = "READY_FOR_TRAINING"
		res.Recommendation = "Dataset meets ML-11 thresholds. Training may proceed on this machine if hardware supports LoRA."
		return res
	}
	res.Ready = false
	res.Status = StatusNotReady
	res.Recommendation = "Collect more REAL sanitized builder executions via BUILDER_TRAINING_DATA_ENABLED=true. " +
		"Do not fabricate data. Re-export builder-semantic-v1 after collection, then re-run the gate."
	return res
}

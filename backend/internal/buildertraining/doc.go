// Package buildertraining runs dataset sufficiency checks, optional
// parameter-efficient fine-tuning, and evaluation for the local builder
// semantic-refinement model.
//
// It never mutates production inference. Training only proceeds when the
// sufficiency gate passes. Model weights must live outside git.
//
// Dependency direction:
//
//	cmd/buildertraining → buildertraining → builderdataset / builderintelligence
package buildertraining

const (
	DatasetVersion = "builder-semantic-v1"
	StatusNotReady = "NOT_READY_FOR_TRAINING"
	StatusDoNotPromote = "DO_NOT_PROMOTE"
	StatusPromote = "PROMOTE"
	StatusTrainedNotPromoted = "TRAINED_NOT_PROMOTED" // trained but awaiting review
	StatusTrainingEnvUnsupported = "TRAINING_ENVIRONMENT_NOT_SUPPORTED"
)

// ReferenceBaselines are ML-11 measured baselines (do not overwrite/reinterpret).
var ReferenceBaselines = []BenchmarkRow{
	{
		Model: "heuristic (ML-11 reference)", Accuracy: 0.82, ProtectedFieldAccuracy: 0.77,
		ClarificationAccuracy: 1.0, ValidJSONPercent: 100, UnsafeOutputPercent: 0,
		P50MS: 0, P95MS: 0.004, Evaluated: true,
		Notes: "ML-11 reference measurement on labeled eval-only set; preserved, not re-interpreted",
	},
	{
		Model: "qwen2.5-0.5b-base (ML-11 reference)", Accuracy: 0.48, ProtectedFieldAccuracy: 0.67,
		ClarificationAccuracy: 1.0, ValidJSONPercent: 100, UnsafeOutputPercent: 7.14,
		P50MS: 340, P95MS: 453, Evaluated: true,
		Notes: "ML-11 reference measurement on labeled eval-only set; preserved, not re-interpreted",
	},
}

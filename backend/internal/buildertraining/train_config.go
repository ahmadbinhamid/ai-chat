package buildertraining

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// FullTrainingConfig is the reproducible LoRA config (executed only when gate+hw pass).
// Fine-tunes the Instruct base checkpoint — NOT the Q4_K_M GGUF inference file.
type FullTrainingConfig struct {
	DatasetVersion string `json:"dataset_version"`
	DatasetHash    string `json:"dataset_hash"`
	SplitHash      string `json:"split_hash"`
	Seed           int64  `json:"seed"`

	BaseModelID         string `json:"base_model_id"`
	InferenceGGUF       string `json:"inference_gguf_note"`
	TrainableArtifact   string `json:"trainable_artifact"`
	Method              string `json:"method"`
	DoNotTrainQuantized bool   `json:"do_not_train_quantized_gguf"`

	MaxSeqLength         int     `json:"max_sequence_length"`
	BatchSize            int     `json:"batch_size"`
	GradientAccumulation int     `json:"gradient_accumulation"`
	LearningRate         float64 `json:"learning_rate"`
	Epochs               float64 `json:"epochs"`
	MaxSteps             int     `json:"max_steps"`

	LoRARank    int      `json:"lora_rank"`
	LoRAAlpha   int      `json:"lora_alpha"`
	LoRADropout float64  `json:"lora_dropout"`
	LoRATargets []string `json:"lora_target_modules"`

	OutputDirOutsideGit string `json:"output_dir_outside_git"`
	TargetSchema        string `json:"target_schema"`
	TaskDescription     string `json:"task_description"`
}

// BuildFullTrainingConfig returns conservative reproducible defaults.
func BuildFullTrainingConfig(stats DatasetStats, outsideGitDir string) FullTrainingConfig {
	if outsideGitDir == "" {
		outsideGitDir = filepath.Join(os.Getenv("HOME"), ".cache", "builder-lm", "lora-runs", DatasetVersion)
	}
	return FullTrainingConfig{
		DatasetVersion: DatasetVersion,
		DatasetHash:    stats.DatasetHash,
		SplitHash:      stats.SplitHash,
		Seed:           42,

		BaseModelID:         "Qwen/Qwen2.5-0.5B-Instruct",
		InferenceGGUF:       "qwen2.5-0.5b-instruct-q4_k_m.gguf is for llama.cpp inference only — do not LoRA-tune the GGUF",
		TrainableArtifact:   "Qwen2.5-0.5B-Instruct (HF safetensors / fp16)",
		Method:              "lora",
		DoNotTrainQuantized: true,

		MaxSeqLength:         512,
		BatchSize:            1,
		GradientAccumulation: 8,
		LearningRate:         2e-4,
		Epochs:               3,
		MaxSteps:             500,

		LoRARank:    8,
		LoRAAlpha:   16,
		LoRADropout: 0.05,
		LoRATargets: []string{"q_proj", "v_proj", "k_proj", "o_proj"},

		OutputDirOutsideGit: outsideGitDir,
		TargetSchema:        `{"constraints":[],"protected_fields":[],"preferences":[],"clarification":"","needs_clarification":false}`,
		TaskDescription:     "User prompt + deterministic BuilderPlan summary → semantic refinement only. Never files, tools, pages.json, or blog generation.",
	}
}

// WriteTrainingManifests writes external-training docs when in-process train is unsupported.
func WriteTrainingManifests(outDir string, stats DatasetStats, cfg FullTrainingConfig, hw HardwareInfo) (manifestPath string, err error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	extDir := filepath.Join(outDir, "external")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		return "", err
	}

	datasetManifest := map[string]any{
		"dataset_version":   DatasetVersion,
		"dataset_hash":      stats.DatasetHash,
		"split_hash":        stats.SplitHash,
		"usable_total":      stats.UsableTotal,
		"train_count":       stats.TrainCount,
		"validation_count":  stats.ValidationCount,
		"test_count":        stats.TestCount,
		"positive_semantic": stats.PositiveSemantic,
		"splits": map[string]string{
			"train":      "train.jsonl",
			"validation": "validation.jsonl",
			"test":       "test.jsonl",
		},
		"note": "Use ML-10 exported splits only. Do not regenerate randomly. Do not fabricate rows.",
	}
	if err := writeJSON(filepath.Join(extDir, "dataset_manifest.json"), datasetManifest); err != nil {
		return "", err
	}
	if err := writeJSON(filepath.Join(outDir, "train_config.json"), cfg); err != nil {
		return "", err
	}
	if err := writeJSON(filepath.Join(extDir, "train_config.json"), cfg); err != nil {
		return "", err
	}

	cmdDoc := fmt.Sprintf(`# External LoRA training (ML-14)

Status: TRAINING_ENVIRONMENT_NOT_SUPPORTED for in-process training on this host.

## Hardware detected
- GOOS=%s GOARCH=%s NumCPU=%d HasCUDA=%v
- cpu_lora_practical=%v
- %s

## Rules
- Fine-tune **%s** (trainable Instruct weights), NOT the Q4_K_M GGUF.
- Task: semantic refinement JSON only (constraints / protected_fields / preferences / clarification).
- Never train the model to edit files, pages.json, tools, or blog content.
- Keep adapters/weights under: %s (outside git).
- Seed=%d max_seq=%d batch=%d grad_accum=%d lr=%g epochs=%g lora_r=%d lora_alpha=%d

## Example (external machine with GPU + PEFT)

`+"```bash"+`
python train_lora_semantic.py \
  --base_model %s \
  --train_file train.jsonl \
  --eval_file validation.jsonl \
  --output_dir %s \
  --seed %d \
  --max_seq_length %d \
  --per_device_train_batch_size %d \
  --gradient_accumulation_steps %d \
  --learning_rate %g \
  --num_train_epochs %g \
  --lora_r %d \
  --lora_alpha %d \
  --lora_dropout %g \
  --target_modules q_proj,v_proj,k_proj,o_proj

# Export adapter; optionally merge + convert to GGUF for llama.cpp offline.
# Do NOT commit weights. Re-run: go run ./cmd/buildertraining evaluate
`+"```"+`

Dataset hash: %s
Split hash: %s
`,
		hw.GOOS, hw.GOARCH, hw.NumCPU, hw.HasCUDA, hw.CPUPractical, hw.Notes,
		cfg.BaseModelID, cfg.OutputDirOutsideGit,
		cfg.Seed, cfg.MaxSeqLength, cfg.BatchSize, cfg.GradientAccumulation, cfg.LearningRate, cfg.Epochs, cfg.LoRARank, cfg.LoRAAlpha,
		cfg.BaseModelID, cfg.OutputDirOutsideGit,
		cfg.Seed, cfg.MaxSeqLength, cfg.BatchSize, cfg.GradientAccumulation, cfg.LearningRate, cfg.Epochs,
		cfg.LoRARank, cfg.LoRAAlpha, cfg.LoRADropout,
		stats.DatasetHash, stats.SplitHash,
	)
	if err := os.WriteFile(filepath.Join(extDir, "EXTERNAL_TRAINING.md"), []byte(cmdDoc), 0o644); err != nil {
		return "", err
	}

	man := map[string]any{
		"status":            StatusTrainingEnvUnsupported,
		"dataset_version":   DatasetVersion,
		"dataset_hash":      stats.DatasetHash,
		"split_hash":        stats.SplitHash,
		"base_model":        cfg.BaseModelID,
		"method":            cfg.Method,
		"seed":              cfg.Seed,
		"hardware":          hw,
		"train_config_path": "train_config.json",
		"dataset_manifest":  "external/dataset_manifest.json",
		"instructions":      "external/EXTERNAL_TRAINING.md",
		"runtime": map[string]any{
			"go_version": runtime.Version(),
			"num_cpu":    runtime.NumCPU(),
		},
	}
	manifestPath = filepath.Join(outDir, "external_training_manifest.json")
	if err := writeJSON(manifestPath, man); err != nil {
		return "", err
	}
	_ = writeJSON(filepath.Join(extDir, "external_training_manifest.json"), man)
	return manifestPath, nil
}

// AttemptTrain runs training only when gate.Ready and hardware is practical.
// Never fabricates weights. On unsupported hardware writes external manifests.
func AttemptTrain(gate GateResult, stats DatasetStats, outDir string) TrainAttemptResult {
	hw := DetectHardware()
	full := BuildFullTrainingConfig(stats, "")
	cfg := DefaultTrainingConfig(stats, full.OutputDirOutsideGit)
	cfg.BaseModel = full.BaseModelID
	cfg.Method = full.Method
	cfg.Seed = full.Seed
	cfg.MaxSteps = full.MaxSteps
	cfg.LearningRate = full.LearningRate

	res := TrainAttemptResult{Hardware: hw, Config: cfg}

	_ = os.MkdirAll(outDir, 0o755)
	_ = writeJSON(filepath.Join(outDir, "train_config.json"), full)

	if !gate.Ready {
		res.Status = StatusNotReady
		res.SkippedReason = StatusNotReady + ": " + gate.Recommendation
		return res
	}

	if !hw.CPUPractical {
		res.Status = StatusTrainingEnvUnsupported
		res.SkippedReason = StatusTrainingEnvUnsupported + ": " + hw.Notes
		res.ExternalTrainReady = true
		if man, err := WriteTrainingManifests(outDir, stats, full, hw); err == nil {
			res.ManifestPath = man
		}
		return res
	}

	res.Status = StatusTrainingEnvUnsupported
	res.SkippedReason = StatusTrainingEnvUnsupported + ": no_in_process_trainer_implemented"
	res.ExternalTrainReady = true
	if man, err := WriteTrainingManifests(outDir, stats, full, hw); err == nil {
		res.ManifestPath = man
	}
	return res
}

// StatusFromTrainAttempt maps train result to a top-level pipeline status when gate passed.
func StatusFromTrainAttempt(train TrainAttemptResult) string {
	if train.Attempted {
		return StatusTrainedNotPromoted
	}
	if train.Status != "" {
		return train.Status
	}
	if strings.Contains(train.SkippedReason, StatusTrainingEnvUnsupported) {
		return StatusTrainingEnvUnsupported
	}
	if strings.Contains(train.SkippedReason, StatusNotReady) {
		return StatusNotReady
	}
	return StatusDoNotPromote
}

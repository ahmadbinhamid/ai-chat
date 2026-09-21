package buildertraining

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"ai-chat/internal/builderdataset"
)

// HardwareInfo records the training host.
type HardwareInfo struct {
	GOOS         string `json:"goos"`
	GOARCH       string `json:"goarch"`
	NumCPU       int    `json:"num_cpu"`
	HasCUDA      bool   `json:"has_cuda"`
	Notes        string `json:"notes"`
	CPUPractical bool   `json:"cpu_lora_practical"`
}

// DetectHardware inspects the process environment (no fabricated GPU claims).
func DetectHardware() HardwareInfo {
	return HardwareInfo{
		GOOS:         runtime.GOOS,
		GOARCH:       runtime.GOARCH,
		NumCPU:       runtime.NumCPU(),
		HasCUDA:      false,
		CPUPractical: false,
		Notes: "CPU/Apple-Silicon laptop assumed. Full LoRA/QLoRA training of Qwen2.5-0.5B " +
			"is not automated in-repo without an explicit external training toolchain. " +
			"Inference via llama.cpp remains supported; training artifacts can be prepared for external runs.",
	}
}

// TrainingConfig documents a reproducible run (executed only when gate passes).
type TrainingConfig struct {
	DatasetVersion string  `json:"dataset_version"`
	DatasetHash    string  `json:"dataset_hash"`
	SplitHash      string  `json:"split_hash"`
	BaseModel      string  `json:"base_model"`
	Quantization   string  `json:"quantization"`
	Method         string  `json:"method"` // lora|qlora|none
	Seed           int64   `json:"seed"`
	MaxSteps       int     `json:"max_steps,omitempty"`
	LearningRate   float64 `json:"learning_rate,omitempty"`
	ArtifactDir    string  `json:"artifact_dir"`
}

// DefaultTrainingConfig returns documented defaults (not executed unless ready).
func DefaultTrainingConfig(stats DatasetStats, artifactDir string) TrainingConfig {
	return TrainingConfig{
		DatasetVersion: DatasetVersion,
		DatasetHash:    stats.DatasetHash,
		SplitHash:      stats.SplitHash,
		BaseModel:      "Qwen2.5-0.5B-Instruct",
		Quantization:   "Q4_K_M",
		Method:         "lora",
		Seed:           42,
		MaxSteps:       500,
		LearningRate:   2e-4,
		ArtifactDir:    artifactDir,
	}
}

// TrainAttemptResult records whether training ran.
type TrainAttemptResult struct {
	Attempted          bool           `json:"attempted"`
	SkippedReason      string         `json:"skipped_reason,omitempty"`
	Hardware           HardwareInfo   `json:"hardware"`
	Config             TrainingConfig `json:"config"`
	ArtifactPath       string         `json:"artifact_path,omitempty"`
	ExternalTrainReady bool           `json:"external_train_ready"`
	ManifestPath       string         `json:"manifest_path,omitempty"`
	Status             string         `json:"status,omitempty"`
}

func hashExamples(examples []builderdataset.TrainingExample) string {
	type row struct {
		K string `json:"k"`
		L string `json:"l"`
		I string `json:"i"`
	}
	rows := make([]row, 0, len(examples))
	for _, ex := range examples {
		rows = append(rows, row{K: ex.SemanticKey, L: string(ex.Label), I: ex.Input.Intent})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].K != rows[j].K {
			return rows[i].K < rows[j].K
		}
		return rows[i].L < rows[j].L
	})
	b, _ := json.Marshal(rows)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hashSplits(train, val, test []builderdataset.TrainingExample) string {
	parts := []string{
		"train:" + hashExamples(train),
		"validation:" + hashExamples(val),
		"test:" + hashExamples(test),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// LoadSplitDir loads train/validation/test JSONL from a builderdataset export dir.
func LoadSplitDir(dir string) (train, val, test []builderdataset.TrainingExample, err error) {
	train, err = readTrainingJSONL(filepath.Join(dir, "train.jsonl"))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("train: %w", err)
	}
	val, err = readTrainingJSONL(filepath.Join(dir, "validation.jsonl"))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("validation: %w", err)
	}
	test, err = readTrainingJSONL(filepath.Join(dir, "test.jsonl"))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("test: %w", err)
	}
	return train, val, test, nil
}

func readTrainingJSONL(path string) ([]builderdataset.TrainingExample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	var out []builderdataset.TrainingExample
	for {
		var ex builderdataset.TrainingExample
		if err := dec.Decode(&ex); err != nil {
			if err == io.EOF {
				break
			}
			return out, err
		}
		out = append(out, ex)
	}
	return out, nil
}

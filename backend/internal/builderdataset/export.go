package builderdataset

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"ai-chat/internal/builderexamples"
)

// ReadExamplesJSONL loads ML-9 Example JSONL from r.
func ReadExamplesJSONL(r io.Reader) ([]builderexamples.Example, error) {
	sc := bufio.NewScanner(r)
	// Allow larger lines (bounded payloads).
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 2*1024*1024)
	var out []builderexamples.Example
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Bytes()
		if len(bytesTrimSpace(line)) == 0 {
			continue
		}
		var ex builderexamples.Example
		if err := json.Unmarshal(line, &ex); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		out = append(out, ex)
	}
	return out, sc.Err()
}

func bytesTrimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\t' || b[j-1] == '\n' || b[j-1] == '\r') {
		j--
	}
	return b[i:j]
}

// WriteSplitJSONL writes one TrainingExample per line.
func WriteSplitJSONL(w io.Writer, examples []TrainingExample) (int, error) {
	enc := json.NewEncoder(w)
	n := 0
	for _, ex := range examples {
		// Strip any accidental production IDs (defense in depth — not on schema).
		if err := enc.Encode(ex); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// WriteReportJSON writes the quality report.
func WriteReportJSON(w io.Writer, report QualityReport) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

// ExportDir writes train/validation/test JSONL + quality report into dir.
func ExportDir(dir string, result TransformResult) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	files := []struct {
		name string
		rows []TrainingExample
	}{
		{"train.jsonl", result.Train},
		{"validation.jsonl", result.Validation},
		{"test.jsonl", result.Test},
	}
	for _, f := range files {
		path := filepath.Join(dir, f.name)
		fh, err := os.Create(path)
		if err != nil {
			return err
		}
		if _, err := WriteSplitJSONL(fh, f.rows); err != nil {
			fh.Close()
			return err
		}
		fh.Close()
	}
	rf, err := os.Create(filepath.Join(dir, "quality_report.json"))
	if err != nil {
		return err
	}
	defer rf.Close()
	if err := WriteReportJSON(rf, result.Report); err != nil {
		return err
	}
	// Also write exclusions summary (fingerprint only).
	ef, err := os.Create(filepath.Join(dir, "exclusions.jsonl"))
	if err != nil {
		return err
	}
	defer ef.Close()
	enc := json.NewEncoder(ef)
	// Stable order
	excl := append([]ExcludedRecord(nil), result.Excluded...)
	sort.SliceStable(excl, func(i, j int) bool {
		if excl[i].Reason != excl[j].Reason {
			return excl[i].Reason < excl[j].Reason
		}
		return excl[i].PromptFingerprint < excl[j].PromptFingerprint
	})
	for _, e := range excl {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return nil
}

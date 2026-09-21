// Command builderdataset transforms ML-9 execution-example JSONL into a
// cleaned, labeled, split training dataset (builder-semantic-v1), and reports
// collection readiness.
//
// Does NOT train or fine-tune any model.
//
// Usage:
//
//	go run ./cmd/builderdataset transform -in examples.jsonl -out ./builder-semantic-v1
//	go run ./cmd/builderdataset status    -in examples.jsonl
//	go run ./cmd/builderdataset status    -dataset ./builder-semantic-v1
//	go run ./cmd/builderdataset snapshot  -in examples.jsonl -out ./builder-semantic-v1
//
// Legacy (still supported):
//
//	go run ./cmd/builderdataset -in examples.jsonl -out ./dataset_out
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"ai-chat/internal/builderdataset"
	"ai-chat/internal/builderexamples"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	if len(os.Args[1]) > 0 && os.Args[1][0] == '-' {
		runTransformLegacy()
		return
	}

	cmd := os.Args[1]
	os.Args = append(os.Args[:1], os.Args[2:]...)
	switch cmd {
	case "transform":
		runTransform()
	case "status":
		runStatus()
	case "snapshot":
		runSnapshot()
	case "help", "-h", "--help":
		printUsage()
	default:
		log.Fatalf("unknown command %q", cmd)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `usage:
  builderdataset transform -in examples.jsonl [-out dir]
  builderdataset status    -in examples.jsonl | -dataset dir [-json]
  builderdataset snapshot  -in examples.jsonl [-out dir]

Does not train models. Does not print raw prompts.`)
}

func runTransformLegacy() {
	inPath := flag.String("in", "", "path to ML-9 examples JSONL (required)")
	outDir := flag.String("out", "builder-semantic-v1", "output directory for splits + report")
	flag.Parse()
	if *inPath == "" {
		log.Fatal("usage: builderdataset -in examples.jsonl [-out dir]")
	}
	doTransform(*inPath, *outDir)
}

func runTransform() {
	fs := flag.NewFlagSet("transform", flag.ExitOnError)
	inPath := fs.String("in", "", "path to ML-9 examples JSONL (required)")
	outDir := fs.String("out", "builder-semantic-v1", "output directory")
	_ = fs.Parse(os.Args[1:])
	if *inPath == "" {
		log.Fatal("transform requires -in")
	}
	doTransform(*inPath, *outDir)
}

func doTransform(inPath, outDir string) {
	result := mustTransformFile(inPath)
	if err := builderdataset.ExportDir(outDir, result); err != nil {
		log.Fatalf("export: %v", err)
	}
	r := result.Report
	fmt.Printf("dataset %s written to %s\n", r.DatasetVersion, outDir)
	fmt.Printf("source=%d usable=%d excluded=%d dup_collapsed=%d\n",
		r.TotalSource, r.Usable, r.Excluded, r.DuplicateCollapsed)
	fmt.Printf("train=%d validation=%d test=%d\n", r.TrainCount, r.ValidationCount, r.TestCount)
	fmt.Printf("positive_semantic=%d positive_routing=%d negative_eval=%d ambiguous_eval=%d\n",
		r.PositiveSemantic, r.PositiveRouting, r.NegativeEval, r.AmbiguousEval)
	for _, note := range r.ImbalanceNotes {
		fmt.Printf("imbalance: %s\n", note)
	}
}

func runStatus() {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	inPath := fs.String("in", "", "source examples JSONL")
	dataset := fs.String("dataset", "", "transformed split directory")
	asJSON := fs.Bool("json", false, "emit JSON readiness report")
	_ = fs.Parse(os.Args[1:])

	var rep builderdataset.ReadinessReport
	switch {
	case *inPath != "":
		examples := mustReadExamples(*inPath)
		rep = builderdataset.BuildReadinessFromSource(examples, "file", 0)
	case *dataset != "":
		train, val, test := mustLoadSplits(*dataset)
		result := rebuildResult(train, val, test)
		rep = builderdataset.BuildReadinessFromTransform(result, "split-dir", 0)
		rep.StoredTotal = len(train) + len(val) + len(test)
	default:
		log.Fatal("status requires -in or -dataset")
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
		return
	}
	fmt.Print(builderdataset.FormatReadinessText(rep))
}

func runSnapshot() {
	fs := flag.NewFlagSet("snapshot", flag.ExitOnError)
	inPath := fs.String("in", "", "source examples JSONL (required)")
	outDir := fs.String("out", "builder-semantic-v1", "output directory for splits + snapshot")
	_ = fs.Parse(os.Args[1:])
	if *inPath == "" {
		log.Fatal("snapshot requires -in")
	}
	examples := mustReadExamples(*inPath)
	result := builderdataset.Transform(examples)
	if err := builderdataset.ExportDir(*outDir, result); err != nil {
		log.Fatalf("export: %v", err)
	}
	rep := builderdataset.BuildReadinessFromSource(examples, "file", 0)
	snap := builderdataset.BuildSnapshot(result, rep)
	if err := builderdataset.WriteSnapshotJSON(*outDir, snap, &rep); err != nil {
		log.Fatalf("snapshot: %v", err)
	}
	fmt.Printf("snapshot written to %s (usable=%d gate=%s)\n",
		filepath.Join(*outDir, "snapshot.json"), snap.UsableCount, snap.GateStatus)
	fmt.Print(builderdataset.FormatReadinessText(rep))
}

func mustTransformFile(inPath string) builderdataset.TransformResult {
	return builderdataset.Transform(mustReadExamples(inPath))
}

func mustReadExamples(path string) []builderexamples.Example {
	f, err := os.Open(path)
	if err != nil {
		log.Fatalf("open input: %v", err)
	}
	defer f.Close()
	examples, err := builderdataset.ReadExamplesJSONL(f)
	if err != nil {
		log.Fatalf("read examples: %v", err)
	}
	return examples
}

func mustLoadSplits(dir string) (train, val, test []builderdataset.TrainingExample) {
	var err error
	train, err = readTrainingJSONL(filepath.Join(dir, "train.jsonl"))
	if err != nil {
		log.Fatalf("train: %v", err)
	}
	val, err = readTrainingJSONL(filepath.Join(dir, "validation.jsonl"))
	if err != nil {
		log.Fatalf("validation: %v", err)
	}
	test, err = readTrainingJSONL(filepath.Join(dir, "test.jsonl"))
	if err != nil {
		log.Fatalf("test: %v", err)
	}
	return train, val, test
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

func rebuildResult(train, val, test []builderdataset.TrainingExample) builderdataset.TransformResult {
	r := builderdataset.QualityReport{
		DatasetVersion:             builderdataset.DatasetVersion,
		TotalSource:                len(train) + len(val) + len(test),
		Usable:                     len(train) + len(val) + len(test),
		TrainCount:                 len(train),
		ValidationCount:            len(val),
		TestCount:                  len(test),
		IntentDistribution:         map[string]int{},
		OperationDistribution:      map[string]int{},
		ConstraintDistribution:     map[string]int{},
		ProtectedFieldDistribution: map[string]int{},
		PreferenceDistribution:     map[string]int{},
		ComplexityDistribution:     map[string]int{},
		SourceDistribution:         map[string]int{},
		ExclusionReasons:           map[string]int{},
	}
	tally := func(ex builderdataset.TrainingExample) {
		switch ex.Label {
		case builderdataset.QualityPositiveSemantic:
			r.PositiveSemantic++
		case builderdataset.QualityPositiveRouting:
			r.PositiveRouting++
		case builderdataset.QualityNegativeEval:
			r.NegativeEval++
		case builderdataset.QualityAmbiguousEval:
			r.AmbiguousEval++
		}
		r.IntentDistribution[ex.Input.Intent]++
		r.SourceDistribution[ex.Metadata.Source]++
	}
	for _, ex := range train {
		tally(ex)
	}
	for _, ex := range val {
		tally(ex)
	}
	for _, ex := range test {
		tally(ex)
	}
	return builderdataset.TransformResult{Train: train, Validation: val, Test: test, Report: r}
}

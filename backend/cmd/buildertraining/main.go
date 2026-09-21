// Command buildertraining runs the ML-14 gate-controlled training + evaluation pipeline.
//
// Usage:
//
//	go run ./cmd/buildertraining gate     [-dataset DIR] [-out DIR]
//	go run ./cmd/buildertraining train    [-dataset DIR] [-out DIR]
//	go run ./cmd/buildertraining evaluate [-dataset DIR] [-out DIR] [-llama-url URL]
//	go run ./cmd/buildertraining report   [-dataset DIR] [-out DIR] [-llama-url URL]
//
// Does NOT enable production BUILDER_LOCAL_LM_ENABLED.
// Does NOT train unless the sufficiency gate passes AND hardware supports it.
// Does NOT fabricate weights.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"ai-chat/internal/buildertraining"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	// Legacy flat flags (no subcommand) → full pipeline with early gate stop.
	if len(os.Args[1]) > 0 && os.Args[1][0] == '-' {
		runLegacyPipeline()
		return
	}

	cmd := os.Args[1]
	os.Args = append(os.Args[:1], os.Args[2:]...)

	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	dataset := fs.String("dataset", "", "path to builder-semantic-v1 split dir")
	out := fs.String("out", filepath.Join("artifacts", buildertraining.DatasetVersion), "artifact output directory")
	llamaURL := fs.String("llama-url", "http://127.0.0.1:8090/v1", "OpenAI-compat llama.cpp base URL")
	evalBase := fs.Bool("eval-base", true, "benchmark Qwen2.5 0.5B base if server reachable")
	fromDB := fs.Bool("from-db", false, "load live builder_execution_examples (dev/staging)")
	tenant := fs.Uint64("tenant", 0, "tenant id for -from-db (0 = all tenants, operator CLI)")
	_ = fs.Parse(os.Args[1:])

	opts := buildertraining.PipelineOptions{
		DatasetDir:  *dataset,
		ArtifactDir: *out,
		LlamaURL:    *llamaURL,
		EvalBaseLM:  *evalBase,
		FromDB:      *fromDB,
		TenantID:    *tenant,
	}

	switch cmd {
	case "gate":
		// Dev convenience: if no dataset path, load live DB examples for readiness.
		if opts.DatasetDir == "" && !opts.FromDB {
			if st, err := os.Stat(buildertraining.DatasetVersion); err != nil || !st.IsDir() {
				opts.FromDB = true
			}
		}
		gate, err := buildertraining.RunGate(opts)
		if err != nil {
			log.Fatalf("gate: %v", err)
		}
		fmt.Print(buildertraining.FormatGateText(gate))
		fmt.Fprintf(os.Stderr, "artifacts=%s\n", *out)
		if !gate.Ready {
			os.Exit(2)
		}
	case "train":
		rep, err := buildertraining.RunTrain(opts)
		if err != nil {
			log.Fatalf("train: %v", err)
		}
		fmt.Println(buildertraining.FormatSummary(rep))
		fmt.Fprintf(os.Stderr, "\nSTATUS=%s artifacts=%s\n", rep.Status, *out)
		exitForStatus(rep.Status)
	case "evaluate":
		rep, err := buildertraining.RunEvaluate(opts)
		if err != nil {
			log.Fatalf("evaluate: %v", err)
		}
		fmt.Println(buildertraining.FormatSummary(rep))
		fmt.Fprintf(os.Stderr, "\nSTATUS=%s artifacts=%s\n", rep.Status, *out)
		exitForStatus(rep.Status)
	case "report":
		rep, err := buildertraining.RunReport(opts)
		if err != nil {
			log.Fatalf("report: %v", err)
		}
		fmt.Println(buildertraining.FormatSummary(rep))
		fmt.Fprintf(os.Stderr, "\nSTATUS=%s artifacts=%s\n", rep.Status, *out)
		exitForStatus(rep.Status)
	case "final-benchmark":
		if opts.DatasetDir == "" && !opts.FromDB {
			opts.FromDB = true
		}
		fb, err := buildertraining.RunFinalBenchmark(buildertraining.FinalBenchmarkOptions{
			ArtifactDir:    *out,
			LlamaURL:       *llamaURL,
			EvalLocalLM:    *evalBase,
			FromDB:         opts.FromDB,
			TenantID:       opts.TenantID,
			DatasetDir:     opts.DatasetDir,
			ShadowURL:      "", // no fabricated candidate
			ShadowProvider: "",
		})
		if err != nil {
			log.Fatalf("final-benchmark: %v", err)
		}
		fmt.Print(buildertraining.FormatFinalBenchmark(fb))
		fmt.Fprintf(os.Stderr, "artifacts=%s\n", *out)
		if fb.ML16Status == buildertraining.StatusNotReady {
			os.Exit(2)
		}
		if fb.ML16Status == buildertraining.StatusDoNotPromote {
			os.Exit(4)
		}
	case "help", "-h", "--help":
		printUsage()
	default:
		log.Fatalf("unknown command %q", cmd)
	}
}

func runLegacyPipeline() {
	dataset := flag.String("dataset", "", "path to builder-semantic-v1 split dir")
	out := flag.String("out", filepath.Join("artifacts", buildertraining.DatasetVersion), "artifact output directory")
	llamaURL := flag.String("llama-url", "http://127.0.0.1:8090/v1", "OpenAI-compat llama.cpp base URL")
	evalBase := flag.Bool("eval-base", true, "benchmark Qwen2.5 0.5B base if server reachable")
	flag.Parse()
	rep, err := buildertraining.RunPipeline(buildertraining.PipelineOptions{
		DatasetDir: *dataset, ArtifactDir: *out, LlamaURL: *llamaURL, EvalBaseLM: *evalBase,
	})
	if err != nil {
		log.Fatalf("pipeline: %v", err)
	}
	fmt.Println(buildertraining.FormatSummary(rep))
	fmt.Fprintf(os.Stderr, "\nSTATUS=%s artifacts=%s\n", rep.Status, *out)
	exitForStatus(rep.Status)
}

func exitForStatus(status string) {
	switch status {
	case buildertraining.StatusNotReady:
		os.Exit(2)
	case buildertraining.StatusTrainingEnvUnsupported:
		os.Exit(3)
	case buildertraining.StatusDoNotPromote:
		os.Exit(4)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `usage:
  buildertraining gate            [-dataset DIR | -from-db [-tenant ID]] [-out DIR]
  buildertraining train           [-dataset DIR | -from-db [-tenant ID]] [-out DIR]
  buildertraining evaluate        [-dataset DIR | -from-db [-tenant ID]] [-out DIR] [-llama-url URL]
  buildertraining report          [-dataset DIR | -from-db [-tenant ID]] [-out DIR] [-llama-url URL]
  buildertraining final-benchmark [-from-db] [-out DIR] [-llama-url URL]

gate             → readiness only
train            → gate then train / env-unsupported
evaluate         → heuristic vs Qwen base vs trained (if any)
report           → promotion / benchmark report
final-benchmark  → ML-16 end-to-end sign-off artifacts (no training, no prod switch)

Does not enable BUILDER_LOCAL_LM_ENABLED. Does not fabricate weights.`)
}

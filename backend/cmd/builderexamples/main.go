// Command builderexamples is a controlled ML-9/ML-13 development tool for
// builder execution example collection: smoke insert, export, and readiness.
//
// It uses the same SQLStore + Collector path as the HTTP server when
// BUILDER_TRAINING_DATA_ENABLED=true. It does NOT enable collection globally
// and does NOT train models.
//
// Usage:
//
//	go run ./cmd/builderexamples smoke      -tenant 1
//	go run ./cmd/builderexamples export     -tenant 1 -out examples.jsonl
//	go run ./cmd/builderexamples status     -tenant 1
//	go run ./cmd/builderexamples readiness  -tenant 1
//	go run ./cmd/builderexamples readiness  -global   # operator CLI only: counts by tenant
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"time"

	"ai-chat/internal/builderdataset"
	"ai-chat/internal/builderexamples"
	"ai-chat/internal/config"
	"ai-chat/internal/db"

	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: builderexamples <smoke|export|status|readiness> [flags]")
		os.Exit(1)
	}
	cmd := os.Args[1]
	os.Args = append(os.Args[:1], os.Args[2:]...)

	cfg := config.Load()
	conn, err := db.Connect(cfg)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer conn.Close()
	store := builderexamples.NewSQLStore(conn)
	ctx := context.Background()

	switch cmd {
	case "smoke":
		runSmoke(ctx, store)
	case "export":
		runExport(ctx, store)
	case "status":
		runStatus(ctx, store)
	case "readiness":
		runReadiness(ctx, store)
	default:
		log.Fatalf("unknown command %q", cmd)
	}
}

func runSmoke(ctx context.Context, store *builderexamples.SQLStore) {
	fs := flag.NewFlagSet("smoke", flag.ExitOnError)
	tenant := fs.Uint64("tenant", 1, "tenant id for the smoke record")
	retention := fs.Int("retention-days", 30, "retention days (trim after insert)")
	maxRec := fs.Int("max-records", 10000, "max records (trim after insert)")
	_ = fs.Parse(os.Args[1:])

	c := builderexamples.NewCollector(builderexamples.Config{
		Enabled:       true,
		SampleRate:    1,
		RetentionDays: *retention,
		MaxRecords:    *maxRec,
	}, store)

	prompt := "change the blogs according to software house but keep JPRO meta titles"
	c.Record(ctx, builderexamples.Input{
		TenantID:     *tenant,
		ChatID:       "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		GenerationID: "ffffffff-1111-2222-3333-444444444444",
		Prompt:       prompt,
		Intent:       "compound",
		Complexity:   "high",
		Compound:     true,
		Operations:   []string{"update_page_content", "update_seo_meta"},
		Targets:      []string{"pages/blog.liquid"},
		Constraints: []string{
			"never_rewrite_unrelated_pages",
			"protect_field:meta_title",
			"preference:software house",
		},
		Context: builderexamples.ContextSnapshot{
			PrimaryFiles:      []string{"pages/blog.liquid"},
			PrimaryCount:      1,
			SchemaFiles:       []string{"pages.json"},
			SchemaCount:       1,
			ContextBytesAfter: 1200,
		},
		GenerationMode:     "edit",
		DeepSeekUsed:       true,
		LocalLMUsed:        false,
		LocalLMSkipped:     true,
		RefinementApplied:  false,
		RefinementRejected: false,
		ToolKinds:          []string{"propose_changes"},
		ToolCount:          1,
		GenerateCalls:      1,
		Model:              "smoke",
		Provider:           "dev",
		WallStatus:         "succeeded",
		HasChanges:         true,
		Performance: builderexamples.PerformanceSnapshot{
			BuilderPlanMs:     5,
			TotalGenerationMs: 42,
			TTFTAvailable:     false,
		},
		Now: time.Now().UTC(),
	})
	if c.Counters.Recorded.Load() != 1 {
		log.Fatalf("smoke insert failed: recorded=%d skipped=%d", c.Counters.Recorded.Load(), c.Counters.Skipped.Load())
	}
	n, err := store.Count(ctx, *tenant)
	if err != nil {
		log.Fatalf("count: %v", err)
	}
	all, _ := store.CountAll(ctx)
	fmt.Printf("smoke_ok tenant=%d tenant_count=%d total=%d fingerprint_only=true\n", *tenant, n, all)
}

func runExport(ctx context.Context, store *builderexamples.SQLStore) {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	tenant := fs.Uint64("tenant", 1, "tenant id")
	out := fs.String("out", "examples.jsonl", "output JSONL path")
	limit := fs.Int("limit", 1000, "max rows")
	_ = fs.Parse(os.Args[1:])

	f, err := os.Create(*out)
	if err != nil {
		log.Fatalf("create: %v", err)
	}
	defer f.Close()
	n, err := builderexamples.ExportJSONL(ctx, store, *tenant, *limit, f)
	if err != nil {
		log.Fatalf("export: %v", err)
	}
	fmt.Printf("exported=%d tenant=%d out=%s\n", n, *tenant, *out)
}

func runStatus(ctx context.Context, store *builderexamples.SQLStore) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	tenant := fs.Uint64("tenant", 1, "tenant id")
	global := fs.Bool("global", false, "operator-only: show per-tenant counts (no payloads)")
	_ = fs.Parse(os.Args[1:])

	if *global {
		counts, err := store.TenantCounts(ctx)
		if err != nil {
			log.Fatalf("tenant counts: %v", err)
		}
		all, _ := store.CountAll(ctx)
		fmt.Printf("global_total=%d tenants=%d\n", all, len(counts))
		ids := make([]uint64, 0, len(counts))
		for tid := range counts {
			ids = append(ids, tid)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, tid := range ids {
			fmt.Printf("  tenant_%d=%d\n", tid, counts[tid])
		}
		return
	}

	n, err := store.Count(ctx, *tenant)
	if err != nil {
		log.Fatalf("count: %v", err)
	}
	all, err := store.CountAll(ctx)
	if err != nil {
		log.Fatalf("count all: %v", err)
	}
	oldest, newest, _ := store.CreatedAtRange(ctx, *tenant)
	fmt.Printf("tenant=%d tenant_count=%d global_total=%d\n", *tenant, n, all)
	if !oldest.IsZero() {
		fmt.Printf("age_range=%s .. %s\n", oldest.UTC().Format(time.RFC3339), newest.UTC().Format(time.RFC3339))
	}
}

func runReadiness(ctx context.Context, store *builderexamples.SQLStore) {
	fs := flag.NewFlagSet("readiness", flag.ExitOnError)
	tenant := fs.Uint64("tenant", 1, "tenant id (required unless -global)")
	global := fs.Bool("global", false, "operator CLI: aggregate all tenants (counts + readiness; no prompts)")
	limit := fs.Int("limit", 10000, "max rows to load for transform")
	asJSON := fs.Bool("json", false, "emit JSON")
	_ = fs.Parse(os.Args[1:])

	var (
		examples []builderexamples.Example
		scope    string
		tid      uint64
	)
	if *global {
		counts, err := store.TenantCounts(ctx)
		if err != nil {
			log.Fatalf("tenant counts: %v", err)
		}
		for tenantID := range counts {
			rows, err := store.ListForExport(ctx, tenantID, *limit)
			if err != nil {
				log.Fatalf("list tenant %d: %v", tenantID, err)
			}
			examples = append(examples, rows...)
		}
		scope = "global-cli"
		tid = 0
	} else {
		rows, err := store.ListForExport(ctx, *tenant, *limit)
		if err != nil {
			log.Fatalf("list: %v", err)
		}
		examples = rows
		scope = fmt.Sprintf("tenant:%d", *tenant)
		tid = *tenant
	}

	rep := builderdataset.BuildReadinessFromSource(examples, scope, tid)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
		return
	}
	fmt.Print(builderdataset.FormatReadinessText(rep))
}

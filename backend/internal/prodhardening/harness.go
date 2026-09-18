package prodhardening

import (
	"sync"
	"sync/atomic"
	"time"
)

// LoadResult is one scenario's measured outcome (Phase 7).
type LoadResult struct {
	Scenario      string
	Concurrency   int
	Generations   int
	Success       int
	Failure       int
	Timeouts      int
	Cancellations int
	P50Ms         int64
	P95Ms         int64
	MaxMs         int64
}

// RunConcurrent runs fn concurrency times and aggregates latencies.
// fn must be deterministic and must not call paid AI providers.
func RunConcurrent(concurrency int, fn func(worker int) (elapsed time.Duration, status string)) LoadResult {
	type sample struct {
		ms     int64
		status string
	}
	samples := make([]sample, concurrency)
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func(i int) {
			defer wg.Done()
			elapsed, status := fn(i)
			samples[i] = sample{ms: elapsed.Milliseconds(), status: status}
		}(i)
	}
	wg.Wait()

	var ms []int64
	r := LoadResult{Concurrency: concurrency, Generations: concurrency}
	for _, s := range samples {
		ms = append(ms, s.ms)
		switch s.status {
		case "ok":
			r.Success++
		case "timeout":
			r.Timeouts++
			r.Failure++
		case "cancelled":
			r.Cancellations++
		default:
			r.Failure++
		}
	}
	r.P50Ms, r.P95Ms, r.MaxMs = percentiles(ms)
	return r
}

func percentiles(ms []int64) (p50, p95, max int64) {
	if len(ms) == 0 {
		return 0, 0, 0
	}
	// Insertion sort — N is small in unit harnesses.
	sorted := append([]int64(nil), ms...)
	for i := 1; i < len(sorted); i++ {
		v := sorted[i]
		j := i
		for j > 0 && sorted[j-1] > v {
			sorted[j] = sorted[j-1]
			j--
		}
		sorted[j] = v
	}
	max = sorted[len(sorted)-1]
	p50 = sorted[(len(sorted)-1)*50/100]
	p95 = sorted[(len(sorted)-1)*95/100]
	return p50, p95, max
}

// GoroutineDelta is a coarse leak signal for repeated stress loops.
type GoroutineDelta struct {
	Before int
	After  int
}

// ProcessCounters are Phase 7 process-scoped observability fields.
var (
	LoadScenariosRun       atomic.Int64
	LoadGenerationsOK      atomic.Int64
	LoadGenerationsFail    atomic.Int64
	LoadCancellations      atomic.Int64
	RateLimiterEvictions   atomic.Int64
	PendingTokensRejected  atomic.Int64
)

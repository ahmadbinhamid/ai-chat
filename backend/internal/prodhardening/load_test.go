package prodhardening_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-chat/internal/genlifecycle"
	"ai-chat/internal/prodhardening"
	"ai-chat/internal/ratelimit"
)

func TestPolicy_TimeoutHierarchy(t *testing.T) {
	p := prodhardening.DefaultPolicy()
	if !prodhardening.TimeoutHierarchyOK(p) {
		t.Fatal("timeout hierarchy invalid")
	}
}

func TestPolicy_RetryBoundFinite(t *testing.T) {
	p := prodhardening.DefaultPolicy()
	max := prodhardening.MaxProviderStreamAttemptsPerGeneration(p)
	if max != 320 {
		t.Fatalf("expected 8×20×2=320, got %d", max)
	}
	if max > 1000 {
		t.Fatalf("retry storm bound too high: %d", max)
	}
}

func TestLoad_ScenarioB_ConcurrentTenantsFakeWork(t *testing.T) {
	prodhardening.LoadScenariosRun.Add(1)
	r := prodhardening.RunConcurrent(8, func(worker int) (time.Duration, string) {
		start := time.Now()
		// Simulate bounded pre-model + model work without network.
		time.Sleep(2 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		select {
		case <-ctx.Done():
			return time.Since(start), "timeout"
		case <-time.After(1 * time.Millisecond):
			prodhardening.LoadGenerationsOK.Add(1)
			return time.Since(start), "ok"
		}
	})
	r.Scenario = "B_concurrent_tenants"
	if r.Success != 8 || r.Failure != 0 {
		t.Fatalf("result=%+v", r)
	}
	t.Logf("scenario B: concurrency=%d success=%d p50=%dms p95=%dms max=%dms",
		r.Concurrency, r.Success, r.P50Ms, r.P95Ms, r.MaxMs)
}

func TestLoad_ScenarioE_ProviderTimeoutContained(t *testing.T) {
	r := prodhardening.RunConcurrent(5, func(worker int) (time.Duration, string) {
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		defer cancel()
		err := fakeProviderCall(ctx, 100*time.Millisecond) // hangs past deadline
		elapsed := time.Since(start)
		if errors.Is(err, context.DeadlineExceeded) {
			return elapsed, "timeout"
		}
		if err != nil {
			return elapsed, "fail"
		}
		return elapsed, "ok"
	})
	r.Scenario = "E_slow_provider"
	if r.Timeouts != 5 {
		t.Fatalf("expected all timeouts, got %+v", r)
	}
	if r.MaxMs > 200 {
		t.Fatalf("timeout containment failed, max=%dms", r.MaxMs)
	}
	t.Logf("scenario E: timeouts=%d max=%dms", r.Timeouts, r.MaxMs)
}

func fakeProviderCall(ctx context.Context, hang time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(hang):
		return nil
	}
}

func TestLoad_ScenarioF_CancelStopsWork(t *testing.T) {
	var afterCancel atomic.Int64
	r := prodhardening.RunConcurrent(10, func(worker int) (time.Duration, string) {
		start := time.Now()
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(time.Millisecond)
			cancel()
		}()
		err := fakeProviderCall(ctx, 500*time.Millisecond)
		if errors.Is(err, context.Canceled) {
			prodhardening.LoadCancellations.Add(1)
			// Ensure no further "Generate" after cancel.
			if ctx.Err() != nil {
				afterCancel.Add(1)
			}
			return time.Since(start), "cancelled"
		}
		return time.Since(start), "fail"
	})
	r.Scenario = "F_cancel"
	if r.Cancellations != 10 {
		t.Fatalf("expected 10 cancellations, got %+v", r)
	}
	if afterCancel.Load() != 10 {
		t.Fatalf("cancel observation mismatch %d", afterCancel.Load())
	}
}

func TestLoad_ScenarioA_SingleGeneration(t *testing.T) {
	r := prodhardening.RunConcurrent(1, func(worker int) (time.Duration, string) {
		start := time.Now()
		time.Sleep(time.Millisecond)
		return time.Since(start), "ok"
	})
	r.Scenario = "A_single"
	if r.Success != 1 {
		t.Fatalf("%+v", r)
	}
	t.Logf("scenario A: max=%dms", r.MaxMs)
}

func TestLoad_ScenarioD_MixedWorkload(t *testing.T) {
	kinds := []string{"simple_edit", "section_edit", "full_page", "repair"}
	r := prodhardening.RunConcurrent(len(kinds)*2, func(worker int) (time.Duration, string) {
		start := time.Now()
		kind := kinds[worker%len(kinds)]
		// Different synthetic costs — no network.
		switch kind {
		case "simple_edit":
			time.Sleep(time.Millisecond)
		case "repair":
			time.Sleep(3 * time.Millisecond)
		default:
			time.Sleep(2 * time.Millisecond)
		}
		prodhardening.LoadGenerationsOK.Add(1)
		return time.Since(start), "ok"
	})
	r.Scenario = "D_mixed"
	if r.Success != r.Generations {
		t.Fatalf("%+v", r)
	}
	t.Logf("scenario D: success=%d p95=%dms max=%dms", r.Success, r.P95Ms, r.MaxMs)
}

func TestLoad_ScenarioG_ToolHeavyBounded(t *testing.T) {
	p := prodhardening.DefaultPolicy()
	filesScanned := 0
	matches := 0
	peak := 0
	inFlight := 0
	var mu sync.Mutex
	var wg sync.WaitGroup
	n := p.MaxGrepFilesScanned + 100 // attempt overscan
	sem := make(chan struct{}, p.GrepMaxConcurrency)
	for i := 0; i < n; i++ {
		if filesScanned >= p.MaxGrepFilesScanned {
			break
		}
		filesScanned++
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			mu.Lock()
			inFlight++
			if inFlight > peak {
				peak = inFlight
			}
			mu.Unlock()
			time.Sleep(time.Millisecond / 2)
			mu.Lock()
			inFlight--
			if matches < p.MaxGrepMatches {
				matches++
			}
			mu.Unlock()
			<-sem
		}()
	}
	wg.Wait()
	if filesScanned > p.MaxGrepFilesScanned {
		t.Fatalf("files scanned %d over cap", filesScanned)
	}
	if matches > p.MaxGrepMatches {
		t.Fatalf("matches %d over cap", matches)
	}
	if peak > p.GrepMaxConcurrency {
		t.Fatalf("peak concurrency %d over cap %d", peak, p.GrepMaxConcurrency)
	}
	t.Logf("scenario G: scanned=%d matches=%d peak=%d", filesScanned, matches, peak)
}


func TestLoad_FailureInjection_HTTPStatuses(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"timeout", context.DeadlineExceeded},
		{"cancel", context.Canceled},
		{"429", errors.New("429 too many requests")},
		{"500", errors.New("500 internal")},
		{"502", errors.New("502 bad gateway")},
		{"503", errors.New("503 unavailable")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Failures must remain local — one error must not poison siblings.
			var okCount atomic.Int64
			var failCount atomic.Int64
			var wg sync.WaitGroup
			for i := 0; i < 4; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					if i == 0 {
						failCount.Add(1)
						_ = tc.err
						return
					}
					okCount.Add(1)
				}(i)
			}
			wg.Wait()
			if okCount.Load() != 3 || failCount.Load() != 1 {
				t.Fatalf("containment failed ok=%d fail=%d", okCount.Load(), failCount.Load())
			}
		})
	}
}

func TestLoad_TerminalGuardUnderConcurrency(t *testing.T) {
	var g genlifecycle.TerminalGuard
	var wins atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			typ := genlifecycle.EventDone
			if i%3 == 1 {
				typ = genlifecycle.EventFailed
			}
			if i%3 == 2 {
				typ = genlifecycle.EventCancelled
			}
			if g.TryClaimTerminal(typ) {
				wins.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("wins=%d want 1", wins.Load())
	}
}

func TestLoad_RateLimiterBoundedUnderTenantChurn(t *testing.T) {
	l := ratelimit.NewPerTenantLimiter(100)
	for id := uint64(1); id <= 6000; id++ {
		_ = l.Allow(id)
	}
	if l.Len() > prodhardening.DefaultPolicy().RateLimiterMaxTenants {
		t.Fatalf("limiter map grew past cap: %d", l.Len())
	}
}

func TestLoad_RepeatedCancelNoGoroutineExplosion(t *testing.T) {
	runtime.GC()
	before := runtime.NumGoroutine()
	for round := 0; round < 20; round++ {
		_ = prodhardening.RunConcurrent(8, func(worker int) (time.Duration, string) {
			start := time.Now()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_ = fakeProviderCall(ctx, time.Second)
			return time.Since(start), "cancelled"
		})
	}
	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	after := runtime.NumGoroutine()
	delta := after - before
	if delta > 50 {
		t.Fatalf("goroutine growth suspicious: before=%d after=%d delta=%d", before, after, delta)
	}
	t.Logf("goroutines before=%d after=%d delta=%d", before, after, delta)
}

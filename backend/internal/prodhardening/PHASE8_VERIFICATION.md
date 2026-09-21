# Phase 8 — Final Production Verification Matrix

Evidence gathered from repository inspection and automated tests on
2026-09-18. Status values are PASS only when both implementation and
tests were confirmed in this phase.

| Phase | Requirement | Implementation | Test/Evidence | Status |
| ----- | ----------- | -------------- | ------------- | ------ |
| 0 | Observability | `internal/ai/metrics.go` TurnMetrics; structured logs in `themebuild/service.go`; `perfmetrics/doc.go` | `ai/metrics_test.go`; generation summary logs | PASS |
| 1 | AI budgets | TokenBudgets, maxToolIterationsCeiling=20, mode paths in generator/service | `provider_compat_test.go`, `iteration_budget_test.go` | PASS |
| 2 | Generation cache | `themefs.CachingStore` 512/20MiB + ThemeKey; Overlay→Cache→base in `doGenerate` | `caching_store_test.go`, service wiring ~L1551 | PASS |
| 3 | Grep / FlowPOS | maxGrep 500/200; concurrency 8; AsCachingStore metrics | `grep_phase3_test.go` | PASS |
| 4 | Repair/retry | shouldStartRepairGenerate; prepareRepairThemeContext; max retries 2 | `repair_decision_test.go`, check_and_repair_test | PASS |
| 5 | DeepSeek | `provider_compat.go` omit cache_control; disable thinking on repair/simple | `provider_compat_test.go` | PASS |
| 6 | UX/WebSocket | TerminalGuard; generation_id wire; bus terminal force; frontend lifecycle | `genlifecycle/*`, stream.go, `generationLifecycle.test.ts` | PASS |
| 7 | Production harden | Policy; rate-limit LRU 4096; pendingTokens soft cap; load harness | `prodhardening/*`, `phase7_*_test.go` | PASS |
| 8 | Sign-off | This matrix + full suite re-run | `phase8_signoff_test.go` + make test/race | PASS |

## Runtime path (verified in code)

```text
POST /messages (rate-limited)
  → Service.Generate enqueue (maxQueueDepth=10)
  → runGeneration / doGenerate
  → OverlayStore(CachingStore(base), draft)
  → intent/mode → Generate / tools / grep
  → generateValidProposal → checkAndRepair
  → emit terminal (done|failed|cancelled) once
  → WebSocket stream + frontend lifecycle
```

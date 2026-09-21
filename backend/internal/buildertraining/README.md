# Builder Training Pipeline (ML-14)

Gate-controlled training + evaluation for **semantic refinement only**.

## Commands

```bash
cd backend

# Readiness only (safe with tiny datasets)
go run ./cmd/buildertraining gate \
  -dataset ./builder-semantic-v1 \
  -out ./artifacts/builder-semantic-v1

# Train only if gate passes (CPU host → TRAINING_ENVIRONMENT_NOT_SUPPORTED + manifests)
go run ./cmd/buildertraining train -dataset ./builder-semantic-v1 -out ./artifacts/builder-semantic-v1

# Evaluate heuristic vs Qwen base (same held-out / eval-only set)
go run ./cmd/buildertraining evaluate -dataset ./builder-semantic-v1 -out ./artifacts/builder-semantic-v1

# Promotion / benchmark report
go run ./cmd/buildertraining report -dataset ./builder-semantic-v1 -out ./artifacts/builder-semantic-v1
```

## Status values

| Status | Meaning |
|--------|---------|
| `NOT_READY_FOR_TRAINING` | Dataset below thresholds |
| `TRAINING_ENVIRONMENT_NOT_SUPPORTED` | Gate passed but no practical in-process LoRA |
| `DO_NOT_PROMOTE` | Trained candidate not better / missing |
| `PROMOTE` | Candidate passes promotion criteria (still no prod switch) |

## Training target

```json
{"constraints":[],"protected_fields":[],"preferences":[],"clarification":"","needs_clarification":false}
```

Do **not** fine-tune the Q4_K_M GGUF. Use `Qwen/Qwen2.5-0.5B-Instruct` trainable weights externally.

## ML-16 final sign-off

```bash
go run ./cmd/buildertraining final-benchmark -out ./artifacts/builder-semantic-v1
```

Writes `final_benchmark.json`, `production_gate.json`, `scenario_results.json`,
`latency_report.json`, `safety_report.json`, `promotion_report.json`, plus
local-ops / local-LM / shadow / DeepSeek / reliability reports.

Does **not** train, promote, or enable `BUILDER_LOCAL_LM_ENABLED`.

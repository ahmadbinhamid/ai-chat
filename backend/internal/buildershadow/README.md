# Builder Shadow Candidate (ML-15)

Runs a **candidate** semantic-refinement model beside production BuilderPlan
traffic. Candidate output is validated, compared, logged, then **discarded**.

```
User → BuilderPlan → Production path → DeepSeek / deterministic
                 └─ SHADOW ONLY → Candidate → Validate → Compare → discard
```

## Defaults

| Flag | Default |
|------|---------|
| `BUILDER_SHADOW_LM_ENABLED` | `false` |
| `BUILDER_LOCAL_LM_ENABLED` | `false` (unchanged) |

Shadow never:
- mutates the production plan
- replaces heuristic / Qwen / DeepSeek
- trains a model
- promotes a candidate

## When there is no trained model

Current state: **no trained candidate**.

- Enabling shadow without `BUILDER_SHADOW_LM_URL` / provider → skip
  (`no_candidate_model_configured`).
- Pointing shadow at the base Qwen endpoint is **eval-only** and still does
  not change the user-visible plan.

## Future trained candidate

1. Host the LoRA (or merged) model on an OpenAI-compatible endpoint.
2. Set:
   ```bash
   BUILDER_SHADOW_LM_ENABLED=true
   BUILDER_SHADOW_LM_PROVIDER=llamacpp
   BUILDER_SHADOW_LM_URL=http://127.0.0.1:8091/v1
   BUILDER_SHADOW_LM_MODEL=<candidate-alias>
   BUILDER_SHADOW_LM_TIMEOUT_MS=1500
   ```
3. Keep `BUILDER_LOCAL_LM_ENABLED=false` until a separate promotion phase.
4. Inspect logs: `ai: buildershadow comparison` / `shadow_*` fields on
   `ai: builderplan observation`.

## Latency

Production path uses non-blocking shadow (`Blocking=false`). The user request
does not wait for the candidate.

# AI Theme Builder — Performance & API Audit

**Repo:** `ai-chat` (Go backend)  
**Date:** 2026-09-16  
**Scope:** Why generations feel slow, where time goes, API response quirks, and searchable improvement ideas.

> How to use this file: search tags like `#latency`, `#effort`, `#tool-loop`, `#grep`, `#retry`, `#api-response`, `#flowpos`, `#queue`.

---

## TL;DR (executive)

Merchant-facing slowness is **mostly not** the HTTP API being slow.

| What | Typical behavior |
|---|---|
| `POST /chats/messages` | Returns **202 in milliseconds** — only enqueue + DB write |
| Actual “AI building” | Background job: **minutes** common; up to **~65 min** timeout |
| Main cost | **LLM time** (effort=`xhigh` + adaptive thinking + multi-iteration tool loop) |
| Secondary cost | **FlowPOS HTTP** per theme file read (esp. `grep_theme`) |
| Tertiary cost | **Retries** (invalid proposal / themecheck repair up to 3× each stage) |

**Bottom line:** The product is designed as async generation + WebSocket progress. Slow UX ≈ long `doGenerate` wall-clock, not slow 202 responses.

---

## 1. End-to-end timeline (what the merchant waits on)

```
Browser
  │
  ├─ POST /api/v1/chats/messages  ──► 202 Accepted (ms)
  │     • auth → FlowPOS /user (cached ~60s)
  │     • RecordUserMessage + EnqueueGeneration
  │     • kick drain loop OR queue behind running gen
  │
  ├─ GET /chats/:id/stream (WebSocket)  ◄── live events
  │     started → fetching_link? → tool progress → proposing
  │     → checking → done | failed | cancelled
  │
  └─ GET /chat (poll fallback) when WS unavailable
```

### Background path (`doGenerate`) — where minutes go

```
doGenerate wall-clock
  ├─ Draft overlay load (DB)
  ├─ Chat history load (DB)
  ├─ Optional: reference URL fetch (+ CSS digest)     #latency #urlfetch
  ├─ buildThemeContext                                  #flowpos
  │     ReadFile(pages.json) + ReadFile(defaults.json)
  │     + ListFiles + GetOrGenerateManifest
  ├─ History summarization if >20 turns                 #history
  ├─ generateValidProposal → ai.Generator.Generate      #tool-loop #effort
  │     up to 28 tool-loop iterations
  │     each iteration = 1 full model stream call
  │     tools: list / read / grep → each hits FlowPOS
  │     retries if invalid / empty-unexplored proposal (≤3)
  ├─ If files changed:
  │     buildSnapshot (more FlowPOS reads)              #flowpos
  │     checkAndRepair (themecheck + up to 3 repair Generate calls)  #retry
  └─ Persist draft file records + emit done
```

**Key log lines to diagnose a slow run in production:**

| Log | Meaning |
|---|---|
| `ai: generation wall-clock` | Total merchant-visible wait for one turn |
| `ai: generate call finished` | One `Generate()`: iterations, model vs tool ms, tokens |
| `ai: model call timing` | Per tool-loop iteration: elapsed, cache hits, reasoning tokens |
| `ai: tool exec timing` | Per tool: FlowPOS/tool latency |
| `ai: tool-loop iteration spent unusually many output tokens on exploration only` | **Thrash** — exploration-only iteration burned huge output |

---

## 2. Why it feels slow — ranked causes

### P0 — Model effort + reasoning tax `#effort` `#latency` `#p0`

**Default config:** `AI_EFFORT=xhigh` (highest), adaptive thinking enabled for Opus-tier models, `AI_MAX_TOKENS=64000`.

**Code:** `backend/internal/config/config.go` (default `"xhigh"`), `backend/internal/ai/generator.go` (`Thinking` + `OutputConfig.Effort`).

**Effect:** Every tool-loop iteration can spend a large fraction of wall-clock on **reasoning/thinking tokens**, not file edits. Code already documents a production case: one exploration-only iteration cost **~24k output tokens / ~285s** (~60% of a 477s generation) with **no file changed** (`thrashOutputTokenThreshold`).

**Improve:**

1. Lower default effort for interactive builder (`medium` / `high`) — keep `xhigh` for evals / hard redesigns only.
2. Mode-based effort: greetings / Q&A / small CSS tweaks → lower; full-page redesign → higher.
3. Cap or earlier-force `propose_changes` when exploration-only thrash is detected (today: **warn only**, no behavior change).
4. Track p50/p95 of `reasoning_tokens` and `model_elapsed_ms` from existing logs.

---

### P0 — Multi-iteration tool loop (serial model calls) `#tool-loop` `#p0`

**Code:** `maxToolIterations = 28`, `forceProposeWithinLastN = 3` in `generator.go`.

Each iteration:

1. Full streaming model call (system prompt + history + prior tool results)
2. Then sequential tool execution
3. Feed results back → next iteration

Worst case: **dozens of model round-trips** before a proposal. Spec already tells the model to batch reads (≤10 paths / call) and finish in few turns — **model compliance is not enforced** beyond late-loop forcing.

**Improve:**

1. Lower `maxToolIterations` for simple modes; keep 28 only for complex.
2. Soft budget: after N exploration iterations or M exploration tool calls, force propose / clarify earlier than last-3.
3. Stronger prompt / tool schema constraints against one-file-at-a-time reads.
4. Metric: `iterations_used` distribution from `ai: generate call finished`.

---

### P1 — `grep_theme` is an N+1 FlowPOS hammer `#grep` `#flowpos` `#p1`

**Code:** `themebuild/tool_exec.go` → `execGrepTheme`.

Behavior:

1. `ListFiles` (1 HTTP)
2. Up to **500** candidate files
3. **Sequential** `ReadFile` per candidate over HTTP to FlowPOS (`storeHTTPTimeout = 60s` each)
4. Stop at 200 matches

One `grep_theme` can mean **hundreds of HTTP round-trips**. Multiple greps per generation multiply this. Tool timing is logged (`ai: tool exec timing`) but the loop does not parallelize reads (unlike `LoadThemeFiles` which uses concurrency=8).

**Improve:**

1. Parallelize grep reads with a bounded worker pool (same pattern as `loadThemeFilesConcurrency = 8`).
2. Prefer FlowPOS-side search API if one exists / can be added (single round-trip).
3. Cache theme file tree + hot file contents **per generation** (in-memory map on the overlay for this `doGenerate` only).
4. Discourage / rate-limit repeated greps in one turn via progress + prompt.

---

### P1 — Every theme touch is remote HTTP `#flowpos` `#p1`

**Code:** `themefs/disk.go` — no shared filesystem; every `ReadFile` / `ListFiles` / `WriteFile` → FlowPOS.

Hot paths that re-hit FlowPOS within one generation:

| Step | Calls |
|---|---|
| `buildThemeContext` | pages + defaults + list + manifest |
| Each `list_theme_files` | ListFiles again |
| Each `read_theme_file` | up to 10 ReadFile (sequential in loop) |
| Each `grep_theme` | ListFiles + up to 500 ReadFile |
| Edit materialization | ReadFile per edit path |
| `buildSnapshot` | ListFiles + 4 core files + each updated path |

**Improve:**

1. **Generation-scoped theme cache:** after first ListFiles / ReadFile in a `doGenerate`, reuse in-memory content for the rest of that generation (invalidate only on writes to draft overlay paths you already own).
2. Parallelize `buildThemeContext` reads (pages/defaults/list/manifest are independent).
3. Parallelize `read_theme_file` path batch (today sequential `for _, p := range args.Paths`).
4. Parallelize `buildSnapshot` required reads.

---

### P1 — Retry multipliers (can 2–3× the whole Generate) `#retry` `#p1`

Two **independent** budgets, each up to `maxThemeCheckRetries + 1` (= **3**) full `Generate` calls:

| Stage | File | What triggers retry |
|---|---|---|
| `generateValidProposal` | `proposal.go` | Invalid proposal OR empty “I changed something” with zero exploration |
| `checkAndRepair` | `proposal.go` | themecheck **error** findings after autofix pass |

A flaky model can theoretically run **many** full tool-loops (expensive). Autofix already avoids some repair trips (boilerplate, asset registration, theme tokens) — good; remaining errors still cost a full Generate.

**Improve:**

1. Separate “cheap repair” vs “full regenerate” paths further (more autofixes).
2. On repair, pass a **narrower** tool set / fewer max iterations.
3. Dashboard: surface “retrying checks…” clearly so wait feels intentional.
4. Alert when `attempts_used > 1` rate spikes.

---

### P2 — Huge system prompt every call `#prompt` `#tokens` `#p2`

**Embedded spec:** `backend/internal/ai/prompts/theme_engine_spec.md` ≈ **42 KB / ~318 lines**, plus dynamic block (pages.json, defaults.json, file tree, component manifest).

Prompt caching is already wired (`cache_control` ephemeral 1h on history breakpoint + dynamic block). Iteration 2+ **should** get `cache_read_input_tokens > 0` if caching works.

**Improve:**

1. Monitor cache hit rate from `ai: model call timing`.
2. Split spec: always-on short rules vs on-demand sections (mode-specific).
3. DeepSeek path: confirm prefix caching still holds with summarization (already a design concern in `.env.example`).

---

### P2 — Chat-level queue serialization `#queue` `#p2`

**Only one generation runs per chat at a time.** Later prompts wait (`queue_position > 0`).

This is correctness (draft overlay consistency), not a bug — but stacked prompts feel like “AI is stuck.”

**Improve:**

1. UX: clear queue UI + cancel.
2. Optional: cancel-previous-on-new-send policy for interactive editing.
3. Do **not** parallelize two writers on the same draft without a redesign.

---

### P2 — Reference URL fetch `#urlfetch` `#p2`

Fetch is correctly **deferred** to background `doGenerate` (not on the 202 path). Still adds wall-clock before the model starts (HTML + stylesheets + digest). Cache exists (`reference_url_cache.go`).

**Improve:** Start `buildThemeContext` in parallel with URL fetch when both needed; ensure cache hit rate is visible in logs.

---

### P3 — History summarization `#history` `#p3`

After **20** turns, older history is collapsed via an extra `Summarize` model call (cached per chat). Fail-open: on error, full history is sent (slower + more tokens).

Usually not the main pain; matters on long threads / DeepSeek prefix cache.

---

### P3 — Auth introspection `#auth` `#api-response`

Every authenticated HTTP request may call FlowPOS `GET /user` (positive cache TTL default **60s**). WebSocket has its own auth path.

Not the generation bottleneck; can add tail latency to **status/poll/apply** under cache miss or FlowPOS slowness (`FLOWPOS_HTTP_TIMEOUT_MS` default 2000).

---

## 3. API response behavior (not “slow JSON” — async product) `#api-response`

### `POST /api/v1/chats/messages` → **202 Accepted**

**Handler:** `handlers/message.go`

Response shape:

```json
{
  "chat": { ... },
  "user_message": { ... },
  "assistant_message": null,
  "generated_files": null,
  "generation_id": "...",
  "queue_position": 0
}
```

**Important:**

- `assistant_message` / `generated_files` are **always null/empty on purpose** on this endpoint.
- Real result arrives later via **WebSocket events** or **GET /chat**.
- If the frontend waits on this POST for the finished design, it will look “broken/slow” even when the API is correct.

### Other latency-sensitive routes

| Route | Sync work | Notes |
|---|---|---|
| `GET /chats/:id/stream` | Long-lived WS | Live progress; needs Redis for multi-replica |
| `GET /chat` | DB + draft summary | Poll fallback |
| `POST .../apply` | Many FlowPOS writes | Blocked if generation running/queued |
| `POST .../preview` | Liquid render + product fetch | Separate from generation |
| `GET /theme-assets/*` | FlowPOS asset bytes | Can be large |

### Failure / empty UX issues (feel like “bugs”) `#ux`

| Issue | Cause in code |
|---|---|
| Long silence | Few events until first tool / first text delta |
| “Nothing happened” after wait | Empty proposal fallback summary after retries |
| Session expired mid-queue | Bearer kept in memory only (`pendingTokens`); pod restart / reaper loses token |
| Multi-replica missed live events | `REDIS_URL` empty → in-process bus only |
| Apply rejected | `ErrApplyBlockedByRunningGeneration` while queue non-empty |

---

## 4. Hard numbers already encoded in the codebase

| Constant / config | Value | Role |
|---|---|---|
| `AI_EFFORT` default | `xhigh` | Max reasoning effort |
| `AI_MAX_TOKENS` default | `64000` | Output cap per model call |
| `maxToolIterations` | `28` | Tool-loop ceiling |
| `forceProposeWithinLastN` | `3` | Force propose near end |
| `maxThemeCheckRetries` | `2` | → up to 3 attempts per stage |
| `generateTimeout` | `65m` | Per-generation wall budget |
| `thrashOutputTokenThreshold` | `5000` | Diagnostic only |
| `maxToolReadPaths` | `10` | Per `read_theme_file` |
| `maxGrepFilesScanned` | `500` | Per `grep_theme` |
| `maxGrepMatches` | `200` | Per `grep_theme` |
| `loadThemeFilesConcurrency` | `8` | Used elsewhere — **not** in grep/read tools |
| `storeHTTPTimeout` | `60s` | Per FlowPOS file HTTP call |
| `summarizeHistoryThreshold` | `20` | History collapse |
| `GENERATION_RATE_LIMIT_PER_MINUTE` | `10` | Per tenant |
| `writeTimeout` (HTTP server) | `30s` | OK because POST no longer runs Claude inline |
| Theme engine spec size | ~42 KB | Static system prompt |

---

## 5. Improvement backlog (searchable checklist)

### Quick wins (config / ops — no big redesign) `#quick-win`

- [ ] **`#effort`** Drop default `AI_EFFORT` from `xhigh` → `high` or `medium` for production builder; A/B latency vs quality.
- [ ] **`#observability`** Dashboard or log query on `ai: generation wall-clock` + `iterations_used` + thrash warnings.
- [ ] **`#redis`** Ensure `REDIS_URL` set in multi-replica so WS progress isn’t “missing” (feels like hang).
- [ ] **`#ux`** Frontend: never block UI on POST body for assistant text; bind to stream events only.
- [ ] **`#ux`** Show queue position + “working: reading files / checking…” from existing event types.

### Medium (code — high ROI) `#improve`

- [ ] **`#grep` `#flowpos`** Parallelize `execGrepTheme` reads (errgroup, concurrency 8).
- [ ] **`#flowpos`** Generation-scoped ReadFile/ListFiles cache inside `doGenerate`.
- [ ] **`#flowpos`** Parallelize `buildThemeContext`, `execReadThemeFile` path loop, `buildSnapshot` core reads.
- [ ] **`#tool-loop`** Act on thrash: after exploration-only + high output tokens, force propose/clarify next iteration (not just warn).
- [ ] **`#retry`** Narrow repair Generate (fewer tools / lower effort / lower max iterations).
- [ ] **`#effort`** Per-mode or per-prompt-class effort selection.

### Larger bets `#architecture`

- [ ] **`#grep`** Server-side theme search in FlowPOS (one RPC).
- [ ] **`#prompt`** Slim / modular system prompt by mode (`brand` / `copy` / `pages` / full).
- [ ] **`#tool-loop`** Speculative: allow parallel tool execution when the model emits multiple tool_use blocks (today sequential in `generator.go`).
- [ ] **`#cache`** Longer-lived theme snapshot cache per tenant+slug with invalidation on apply (careful with draft overlay semantics).

---

## 6. What is already done well (don’t “fix” these blindly)

1. **Async 202** — POST no longer blocks on Claude (`cmd/server/main.go` writeTimeout comment).
2. **URL fetch deferred** to background (doesn’t stall enqueue).
3. **Prompt caching** breakpoints on history + dynamic system block.
4. **History summary cache** for long chats / DeepSeek prefix stability.
5. **themecheck autofix** for mechanical failures (avoids some expensive repair rounds).
6. **Draft overlay** so multi-turn edits don’t write live theme until Apply.
7. **Diagnostics logs** already separate model vs tool elapsed time — use them before guessing.

---

## 7. Suggested measurement plan (before changing code)

For 20–50 real generations, collect:

1. `elapsed_ms` from `ai: generation wall-clock`
2. `model_elapsed_ms` vs `tool_elapsed_ms` from `ai: generate call finished`
3. `iterations_used`
4. Count of thrash warnings
5. `cache_read_input_tokens` on iteration ≥1
6. `attempts_used` from `generateValidProposal` / `checkAndRepair`
7. Per-tool p95 from `ai: tool exec timing` (especially `grep_theme`)

**Decision rule:**

- If `model_elapsed_ms` ≫ `tool_elapsed_ms` → prioritize **effort / tool-loop / thrash** (`#effort` `#tool-loop`).
- If `tool_elapsed_ms` dominates → prioritize **grep parallelism + theme cache** (`#grep` `#flowpos`).
- If `attempts_used` often >1 → prioritize **validation/autofix / repair scope** (`#retry`).

---

## 8. Key file map

| Area | Path |
|---|---|
| HTTP routes / wiring | `backend/internal/server/server.go` |
| Send message (202) | `backend/internal/server/handlers/message.go` |
| Enqueue + doGenerate | `backend/internal/modules/themebuild/service.go` |
| Tool loop / effort / thrash | `backend/internal/ai/generator.go` |
| Theme tools (read/list/grep) | `backend/internal/modules/themebuild/tool_exec.go` |
| Proposal + check retries | `backend/internal/modules/themebuild/proposal.go` |
| FlowPOS file HTTP | `backend/internal/themefs/disk.go` |
| Config defaults | `backend/internal/config/config.go`, `backend/.env.example` |
| Theme engine prompt | `backend/internal/ai/prompts/theme_engine_spec.md` |
| Standing eng rules | `CLAUDE.md` |

---

## 9. One-sentence answer

**AI builder “slow” hai kyunki kaam itself minutes-long LLM tool-loops pe chalta hai (`xhigh` effort + up to 28 iterations + optional repair regenerations), aur har theme read FlowPOS HTTP pe jati hai — `POST /chats/messages` pehle se fast 202 return karta hai; asli wait background generation + stream pe hai.**

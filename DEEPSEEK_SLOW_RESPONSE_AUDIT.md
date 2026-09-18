# AI Chat Builder — DeepSeek Slow Response: Deep Performance Audit

**Repo:** `ai-chat` (Go) + frontend `tenant-dashboard` (stream/send UX)  
**Date:** 2026-09-16  
**Constraint:** Diagnosis only. No code changes. No optimizations. No PR.

**Method:** Every claim below is traced to a concrete file/function. Where production numbers are required, the report states what logs already prove vs what must still be measured.

---

## Executive Diagnosis

**DeepSeek is not uniquely wired as a “slow path.”** When `AI_PROVIDER=deepseek`, the process uses the **same** Anthropic Messages API client, the **same** tool loop, the **same** FlowPOS theme HTTP, and the **same** retry/repair multipliers as Anthropic. The only differences at init are API key, base URL (`https://api.deepseek.com/anthropic`), and model string (`deepseek-v4-pro` by default).

What the code **does** prove without production samples:

1. **Merchant wait is almost entirely `doGenerate` wall-clock**, not `POST /chats/messages` (that returns **202** after enqueue).
2. **Default effort is `xhigh` and adaptive thinking is enabled for `deepseek-v4-pro`** (anything whose name does not contain `"haiku"`). That is passed to DeepSeek as `thinking: adaptive` + `output_config.effort`.
3. **Every tool-loop iteration is a full streamed LLM request**; tools run **sequentially**; prior tool results stay in `messages` and grow context.
4. **`grep_theme` is worst-case 1 + 500 FlowPOS HTTP GETs, sequential**; no per-generation ReadFile cache.
5. **Before the first DeepSeek call**, `buildThemeContext` + `buildSnapshotBase` already duplicate `ListFiles` and re-read `pages.json`/`defaults.json`.
6. **Retries can multiply full tool loops**: up to **3** `Generate` calls in `generateValidProposal` + up to **2** repair `Generate` calls in `checkAndRepair` ⇒ theoretical **5** full loops × **28** iterations each.
7. **DeepSeek-specific behavior confirmed in code comments/tests:** `cache_control` is **silently ignored**; caching is prefix-match based; `tool_choice` forcing is **not reliably honored** (extra nudge iterations).
8. **Frontend correctly treats POST as async** and opens a WebSocket while busy. Perceived freezes are still possible during long pre-tool setup or long thinking before the first `tool_call`/`thinking` event.

**What cannot be proven from code alone:** whether *your* slow DeepSeek runs are dominated by model time vs FlowPOS vs retries. The instrumentation already separates `model_elapsed_ms` vs `tool_elapsed_ms` — that ratio is the decisive measurement (§Measurements Still Required).

**Working root-cause ranking (evidence-weighted, pending log confirmation):**

| Likely rank | Category | Why code points here |
|---|---|---|
| 1 | **A — Model latency** | `xhigh` + adaptive thinking on every iteration; documented thrash case ~285s exploration-only; DeepSeek may emit large reasoning |
| 2 | **A×serial tool loop** | Up to 28 serial model calls per `Generate`; DeepSeek zero-tool nudges burn extra iterations |
| 3 | **B — FlowPOS/`grep_theme`** | N+1 sequential reads; duplicated context/snapshot fetches |
| 4 | **C — Retry amplification** | Up to 5 full `Generate` loops |
| 5 | **D — Prompt/cache** | ~43KB static spec always resent; DeepSeek ignores `cache_control` (prefix cache only) |
| 6 | **E/F** | Queue serialization / UX silence — real but secondary to multi-minute model loops |

---

## Exact Generation Call Graph

### Synchronous HTTP path (fast)

```
POST /api/v1/chats/messages
  handlers.MessageHandler.Send                         [handlers/message.go]
    auth.Middleware → FlowPOS GET /user (cached)       [auth/middleware.go]
    ratelimit.PerTenantLimiter.Allow
    themebuild.Service.Generate                        [themebuild/service.go]
      Validate images / optional ExtractReferenceURL (no network fetch)
      chat.Service.GetOrCreateChat
      chat.Service.RecordUserMessage                   [MySQL]
      repo.EnqueueGeneration                           [MySQL]
      tokens.store(genID, bearer)                      [in-memory only]
      repo.DequeueNext
        ├─ success → go runGeneration(...)             [ASYNC BOUNDARY]
        └─ ErrGenerationInProgress → emit "queued"
    httpresponse.Accepted 202
      { chat, user_message, assistant_message:null,
        generated_files:null, generation_id, queue_position }
```

**Async boundary:** `go func(){ s.runGeneration(context.WithoutCancel(ctx), c, next) }` in `Service.Generate`. Request context cancellation does **not** cancel the generation.

### Asynchronous generation path (slow)

```
runGeneration → loop drain queue
  runOneQueuedGeneration
    emit "dequeued"
    tokens.take(genID)  // fail → session-expired if missing
    context.WithTimeout(..., generateTimeout=65m)
    heartbeat ticker + cancel listener
    doGenerate(ctx, in, chat, genID, cancelledByUser)
      emit "started"
      DraftFiles → OverlayStore
      ListMessages (+ optional GetAttachmentsContent)
      optional fetchReferenceURL (+ emit fetching_link/fetched_link)
      optional HTML carry-forward
      buildThemeContext          // FlowPOS: pages, defaults, ListFiles, manifest
      buildSnapshotBase          // FlowPOS: ListFiles AGAIN + pages/defaults/layouts
      summarizeOldTurnsCached    // optional extra Summarize model call if >20 turns
      generateValidProposal
        loop ≤ maxThemeCheckRetries+1 (=3):
          ai.Generator.Generate  // DEEPSEEK STREAMING TOOL LOOP
      if proposalHasChanges:
        emit "proposing"
        buildSnapshot (extra ReadFile per updated path)
        checkAndRepair
          loop attempts:
            themecheck.Check (+ autofix mechanical rules)
            on error findings & budget left:
              emit "repairing"
              ai.Generator.Generate again  // FULL LOOP
      themeLocks.Lock → buildWritePlan (reads) → emit "staged"
      RecordAssistantMessage + persistFileRecords (draft only; no live theme write)
      emit "done" | "failed" | "cancelled"
```

### Inside one `ai.Generator.Generate` (DeepSeek)

```
ai.Generator.Generate                                  [ai/generator.go]
  build messages from history + new prompt (+ images)
  system = [staticSystemPromptBlock(spec+rules), dynamicSystemPrompt(tc)]
  for iteration in 0..maxToolIterations-1 (=28):
    if iteration >= 25: force ToolChoice=propose_changes
    if modelSupportsAdaptiveThinking(model):  // true for deepseek-v4-pro
      Thinking=adaptive, OutputConfig.Effort=g.effort  // default xhigh
    NewStreaming(ctx, params)  // DeepSeek HTTP stream
      retry up to streamAccumulateMaxAttempts(=3) on accumulate errors
      (+ 5s delay between retries)
    parse tool_use blocks
    if propose_changes OK → materializeEdits → return Result
    else execute each tool_use SEQUENTIALLY via ToolExecutor:
      list_theme_files | read_theme_file | grep_theme | validate_changes
    append model turn + tool_results to messages  // context grows
```

### Live progress (parallel to above)

```
GET /api/v1/chats/:chatId/stream  (WebSocket)
  handlers.StreamHandler.Stream
  auth via Sec-WebSocket-Protocol (not Authorization header)
  replay generation_events + subscribe eventBus (+ Redis if configured)
```

Frontend: `tenant-dashboard/.../useAiChatSession.ts` + `useGenerationStream.ts` + `api/ai-chat-stream.ts`.

---

## DeepSeek API Audit

### How DeepSeek is selected

| Step | Code |
|---|---|
| Config | `config.Load`: `AI_PROVIDER` must be `anthropic`\|`deepseek` |
| Init | `server.New`: `case cfg.AIProvider == "deepseek": ai.New(DeepSeekAPIKey, DeepSeekBaseURL, DeepSeekModel, Effort, DeepSeekVisionModel, MaxTokens)` |
| Defaults | Model `deepseek-v4-pro`; Base URL `https://api.deepseek.com/anthropic`; Vision `deepseek-v4-flash-vision-exp` |

**Same `ai.New` as Anthropic** — only `baseURL` non-empty for DeepSeek (`option.WithBaseURL`).

### Answers to the required DeepSeek questions

| # | Question | Answer from code |
|---|---|---|
| 1 | Is DeepSeek actually called? | **Yes, iff** `AI_PROVIDER=deepseek` and not `AI_CHAT_FAKE_MODE`. Startup log: `ai: generator configured` with `base_url_set=true`. |
| 2 | Exact model? | Default **`deepseek-v4-pro`** (`DEEPSEEK_MODEL`). Image turns use **`deepseek-v4-flash-vision-exp`**. |
| 3 | Is reasoning enabled? | **Yes for default model.** `modelSupportsAdaptiveThinking` returns false only if model name contains `"haiku"`. `deepseek-v4-pro` → adaptive thinking ON. |
| 4 | Output/reasoning tokens requested? | `MaxTokens` (default **64000**) is the API `max_tokens`. Reasoning is not a separate quota field; effort is `OutputConfig.Effort`. Actual reasoning usage is observed via `Usage.OutputTokensDetails.ThinkingTokens` when Valid. |
| 5 | Is `AI_MAX_TOKENS=64000` passed? | **Yes** — `params.MaxTokens: g.maxTokens` every iteration (`generator.go`). |
| 6 | Is `AI_EFFORT=xhigh` meaningful for DeepSeek? | **Sent on the wire** when thinking is enabled. Code comments + `vision_smoke_test.go` treat DeepSeek compat as supporting thinking/effort. Whether DeepSeek *internally* maps `xhigh` identically to Anthropic is **outside this repo** — must measure `reasoning_tokens` / `model_elapsed_ms`. Config comment explicitly warns: naming “Anthropic-only” wrongly ruled out DeepSeek latency once. |
| 7 | Unnecessary parameters? | `cache_control` on static + dynamic system + history breakpoint is set always; DeepSeek **silently ignores** it (`ai.New` doc). Still sent. No temperature set (SDK default). |
| 8 | Streaming correct? | `Messages.NewStreaming` + `message.Accumulate`; delta coalescer for UI. Accumulate failures retry (3×, 5s). |
| 9 | HTTP connection reuse across iterations? | **One `anthropic.Client` per process** on `Generator`. Iterations reuse that client. Connection pooling is the SDK/default `http.Transport` behavior (module not present locally to inspect further). |
| 10 | Connection setup per Generate? | No new client per Generate/iteration. Possible TLS handshake cost only on cold pool / idle timeout — not per design. |
| 11 | Retry around DeepSeek request? | **Yes:** `streamAccumulateMaxAttempts=3` on “accumulate stream” errors only. Not on generic HTTP 5xx unless Accumulate surfaces them that way. |
| 12 | Retries invisible to generation metrics? | **Partially.** Per-iteration `ai: model call timing` includes `attempts_used` and folds retry wait into `elapsed_ms` for that iteration. Summary `model_elapsed_ms` **includes** retry sleeps. There is **no** separate “provider_retry_wait_ms” counter. |
| 13 | Prompt caching supported/hit? | **Anthropic `cache_control`: ignored by DeepSeek.** DeepSeek caches by **request-prefix match** (documented in `config.HistorySummarizationEnabled` + `vision_smoke_test.go`). Hits are **not guaranteed** by `cache_control`. |
| 14 | Cache hits visible in logs? | Fields logged: `cache_read_input_tokens`, `cache_creation_input_tokens` on `ai: model call timing`. Whether DeepSeek populates them must be verified in real logs (`> 0` or always 0). |
| 15 | Large system prompt resent every iteration? | **Yes in the request payload.** Static ~43KB spec + dynamic theme block rebuilt every iteration. Intended Anthropic cache would avoid *billing/reprocess*; DeepSeek ignores `cache_control`, so prefix stability matters more. |
| 16 | History duplicated? | Chat history turns once at start; **within a Generate**, each iteration appends prior assistant+tool turns — tool history accumulates. Across repair `Generate` calls, `turns` includes recap of rejected proposals (`recapAssistantTurn` can embed full file contents). |
| 17 | Tool results huge input growth? | **Yes by design.** `read_theme_file` up to **40_000 bytes** total per call; `grep_theme` up to **200** match lines; `list_theme_files` full tree JSON; `validate_changes` accepts full proposal schema (can be very large). All appended to `messages`. |

### Provider comparison (same workload?)

| Dimension | Anthropic path | DeepSeek path |
|---|---|---|
| Client | `ai.New(key, "", model, …)` | `ai.New(key, deepseekBaseURL, model, …)` |
| Wire protocol | Anthropic Messages API | Same (compat endpoint) |
| Tool loop | Identical | Identical |
| Effort / max_tokens | Same env `AI_EFFORT` / `AI_MAX_TOKENS` | Same |
| Adaptive thinking | Opus/Sonnet-tier | Same gate (`!haiku`) |
| `tool_choice` | Honored (per comments) | **Not reliably** → zero-tool nudge path |
| `cache_control` | Honored | **Silently ignored**; prefix cache instead |
| History summarization | Enabled by default | Enabled; **more important** for prefix cache stability |
| FlowPOS / themecheck / retries | Identical | Identical |

**Conclusion:** DeepSeek does **not** receive a lighter workload. It receives the **same** heavy loop, plus known compat frictions (tool_choice, cache_control).

---

## Tool Loop Audit

| Parameter | Value | Location |
|---|---|---|
| `maxToolIterations` | **28** | `ai/generator.go` |
| `forceProposeWithinLastN` | **3** (force from iteration 25) | same |
| `thrashOutputTokenThreshold` | **5000** output tokens | same |
| Thrash behavior | **`slog.Warn` only — no control-flow change** | documented explicitly |
| Tool parallelism | **None** — `for _, tu := range toolUses` sequential | `Generate` |
| Tools (non-brand) | `list_theme_files`, `read_theme_file`, `grep_theme`, `validate_changes`, `propose_changes` | `tools.go` |
| Brand mode | `propose_changes` only | `toolsForMode` |

**Continue exploring:** Model chooses tools under `ToolChoiceAny` until forced. No server-side ban on re-reading the same file. Spec instructs batching; **not enforced**.

**Minimum iterations:** 1 (propose on first response).  
**Typical:** Unknown without logs (`iterations_used`).  
**Maximum model streams per `Generate`:** 28 × up to 3 accumulate attempts.

**`validate_changes`:** Cap **4 calls/turn** (`maxValidateChangesCalls`). Not in `explorationToolNames`, so thrash detector **ignores** validate-heavy iterations. Each call may `MaterializeEdits` (more ReadFile) + `buildSnapshot` + `themecheck.Check`.

### Theoretical max DeepSeek calls for one user request

Let `G` = one full `Generator.Generate` (≤28 streamed model calls).

| Stage | Max `G` |
|---|---|
| `generateValidProposal` | 3 |
| `checkAndRepair` repair Generations | 2 |
| Optional history `Summarize` | +1 cheap non-tool call (not a full tool loop) |
| **Total full tool-loop Generations** | **≤ 5** |
| **Total streamed DeepSeek requests** | **≤ 5 × 28 = 140** (× ≤3 if every stream accumulate fails/retries) |

Realistic “bad but possible” (invalid proposal once + one repair): **2–3** full loops × several iterations.

---

## FlowPOS HTTP Audit

**Client:** `themefs.Store` — `http.Client{Timeout: 60s}`, base `FLOWPOS_API_BASE`.  
**Auth:** `Authorization: Bearer` + `TID` header.

| Operation | HTTP | Retry | Parallelism | Cache |
|---|---|---|---|---|
| `ReadFile` | `GET .../store/themes/active/files/{path}` | 3 attempts on network/5xx (300ms, 900ms) | single | **None** (draft overlay only if pending draft has path) |
| `ListFiles` | `GET .../store/themes/active/files` | no read-retry helper | single | **None** |
| `WriteFile` | `POST` | **no retry** | single | n/a (Apply path, not generate) |
| `DeleteFile` | `DELETE` | none | single | n/a |

### Mapped generation operations

| Call site | FlowPOS work |
|---|---|
| `buildThemeContext` | ReadFile×2 (pages, defaults) + ListFiles + GetOrGenerateManifest (may read many component files) |
| `buildSnapshotBase` | **ListFiles again** + ReadFile×4 (pages, defaults, layout-start/end) |
| `list_theme_files` | ListFiles |
| `read_theme_file` | up to **10 sequential** ReadFile |
| `grep_theme` | ListFiles + up to **500 sequential** ReadFile |
| `validate_changes` / materialize | ReadFile per edit target + baselines |
| `buildSnapshot` (post-propose) | ReadFile per `update` path not already in base |
| `buildWritePlan` | more reads for layout splice |

**Repeated fetches:** Same path can be ReadFile’d many times in one generation (no generation-scoped content cache). Overlay only skips FlowPOS if that path is already in **draft**.

### `grep_theme` worst case

Per call (`execGrepTheme`):

1. `ListFiles` → **1** HTTP  
2. Up to `maxGrepFilesScanned=500` candidates  
3. **Sequential** `ReadFile` each → **≤500** HTTP  

**Worst case per `grep_theme` = 501 HTTP requests.**

Per `Generate`: model can invoke many greps across ≤28 iterations; multiple greps in **one** iteration run sequentially (observed thrash note: **6 greps in one iteration**).

Theoretical absurd upper bound (not expected): 28 iterations × N greps × 501 — bounded practically by time/timeout (65m) and model behavior.

`LoadThemeFiles` concurrency (=8) exists elsewhere but is **not** used by `execGrepTheme` / `execReadThemeFile`.

---

## Prompt / Token / Cache Audit

| Component | Size / behavior | Code |
|---|---|---|
| Static system | Embedded `theme_engine_spec.md` ≈ **42.9 KB** + fixed rules; `cache_control` TTL 1h | `staticSystemPromptBlock` |
| Dynamic system | Theme slug, mode, pages.json, defaults.json, file tree, manifest | `dynamicSystemPrompt` — also given `cache_control` in `Generate` despite older comment saying otherwise |
| History | Up to 20 recent turns (+ optional summary turn) | `summarizeHistoryThreshold=20` |
| Per iteration growth | Full prior tool_use + tool_result transcript retained | `messages` append in loop |
| Read cap | 40_000 bytes / read_theme_file call | `maxToolReadBytes` |
| Propose / validate payload | Full file contents in tool JSON | `resultSchema` |

**DeepSeek cache reality (code-stated):**

- `cache_control` **ignored**.
- Prefix-byte stability matters; history summarization was fixed to be **cached** so summaries don’t reshuffle the prefix every call.
- Log fields exist; **must confirm** `cache_read_input_tokens > 0` on DeepSeek in production.

**Duplication before first model call:** `pages.json`/`defaults.json`/`ListFiles` fetched in both `buildThemeContext` and `buildSnapshotBase`.

---

## Retry Audit

| Mechanism | Trigger | Max extra cost |
|---|---|---|
| `generateValidProposal` | `validateProposal` fail OR `isUnexploredEmptyProposal` | Up to **3** full `Generate` |
| `checkAndRepair` | themecheck **error** findings after autofix | Up to **2** additional full `Generate` (attempts 1–2 repair; attempt 3 fails closed) |
| Autofix (free) | missing boilerplate / asset registration / theme tokens | **0** model calls |
| Stream accumulate retry | truncated stream | Up to +10s wait + re-stream **same** iteration |
| DeepSeek zero-tool nudge | no tool_use despite tool_choice | Burns an iteration; continues loop |
| Edit materialize failure | bad old_string/etc. | Continues loop with error tool_result (same Generate) |

**Normal request can run several complete DeepSeek tool loops:** Yes — especially page redesigns that fail themecheck colors/structure, or flaky empty proposals.

---

## Frontend UX Audit

**Repo:** `tenant-dashboard` (not inside `ai-chat`).

| Behavior | Evidence |
|---|---|
| POST treated as async | `api-types.ts`: assistant_message always null; `useAiChatSession` `onSuccess` only appends user message + queue entry |
| Progress via WebSocket | `useGenerationStream` opens when `isBusy`; `streamGeneration` |
| Poll fallback | Only if `onUnavailable` after reconnect exhaustion; `getChatStatus` every 3s |
| Steps UI | `reduceSteps` on `tool_call`/`tool_result`/checking/repairing; `MAX_VISIBLE_STEPS=8` |
| Thinking text | `EventTypeThinking` via `emitLive` (ephemeral); coalesced ≤200ms / 80 chars |

**Silent periods (perceived latency ≠ backend idle):**

1. After POST until WS `started`/`dequeued` — usually short if stream already open / opens on busy.  
2. After `started` until first `thinking`/`tool_call` — includes **theme context + snapshot base + optional URL fetch + history summarize** with **no** granular events (only `started`, maybe link fetch). This can look frozen.  
3. During long reasoning before first text/thinking block — UI waits on stream; if DeepSeek thinking blocks don’t deserialize as `ThinkingBlock`, `currentText` may stay empty longer (code explicitly defensive about provider variance).  
4. Multi-replica without Redis: live events may miss → `streamUnavailable` → 3s poll (feels stuck).

**Verdict:** Frontend architecture is correct for async generation. It can still **amplify perception** of a 2–10 minute model job as “hung” during pre-tool setup or quiet reasoning, but it does **not** block on POST for the finished answer.

---

## Latency Budget

```text
User request
  ↓
POST /chats/messages .................... expected: tens–hundreds ms
  auth (/user cache hit) ............... ~0–2s on miss (FLOWPOS_HTTP_TIMEOUT_MS=2000)
  DB enqueue ........................... ~ms–tens ms
  ↓
queue wait ............................. 0 if idle; else sum of prior gens (up to 65m each)
  ↓
doGenerate start ("started")
  draft + history DB ................... usually small
  reference URL fetch .................. 0–10s+ (urlfetch); cached possible
  buildThemeContext .................... serial FlowPOS (4+ calls; manifest may add many)
  buildSnapshotBase .................... serial FlowPOS AGAIN (ListFiles + 4 reads)
  history summarize .................... 0 or 1 extra model call if >20 turns
  ↓
DeepSeek Generate #1 (tool loop)
  per iteration:
    model stream ....................... DOMINANT UNKNOWN (effort xhigh)
    tools sequential ................... grep can dominate tools side
  ≤28 iterations; force propose at 25+
  ↓
optional Generate #2/#3 (invalid/empty proposal)
  ↓
themecheck (+ autofix)
  ↓
optional repair Generate(s) ............ full loops again
  ↓
stage draft + persist + "done"
```

| Stage | Serial/Parallel | Expected | Worst |
|---|---|---|---|
| POST | serial | &lt;1s | rate-limit / auth 503 |
| Queue wait | serial per chat | 0 | hours if backlog |
| Pre-model FlowPOS | **serial, duplicated** | ~0.5–5s | tens of s if FlowPOS slow |
| Model iteration | serial | seconds–minutes | documented 285s single exploration iteration |
| Tools / grep | serial N+1 | ms–seconds | minutes if many greps × many files |
| Retries | serial full loops | 0 | ×3–5 wall clocks |
| Persist/events | serial | &lt;1s | — |

**HTTP server `writeTimeout=30s` does not bound generation** — generation is background.

---

## Root Causes Ranked by Evidence

| Priority | Issue | Evidence | Estimated Impact | Exact Code Location |
|---|---|---|---|---|
| **P0** | Default `AI_EFFORT=xhigh` + adaptive thinking on DeepSeek default model | Config default `"xhigh"`; thinking enabled for non-haiku; effort applied every iteration; config comment admits DeepSeek latency was mis-attributed | **Very high** — multiplies cost of every one of ≤28 iterations | `config/config.go` `Effort`; `ai/generator.go` `modelSupportsAdaptiveThinking`, `params.Thinking` / `OutputConfig` |
| **P0** | Serial tool-loop: each iteration = full DeepSeek stream; context accumulates | `for iteration := 0; iteration < 28`; messages append tool results; tools sequential | **Very high** — wall-clock ≈ Σ model_i + Σ tool_i | `ai/generator.go` `Generate` |
| **P0** | Exploration thrash (huge output tokens, no propose) — warn-only | Production note: 24,315 output tokens / 285s / 6 greps; `thrashOutputTokenThreshold` does not alter behavior | **High when it occurs** (can be majority of a run) | `ai/generator.go` thrash warn |
| **P1** | DeepSeek compat: `tool_choice` not reliable → nudge iterations | Explicit `New`/`Generate` comments + tests for toolless hello | **Medium–high** on short prompts; adds wasted model calls | `ai/generator.go` zero-tool nudge |
| **P1** | DeepSeek ignores `cache_control`; large static+dynamic prompt every call | `ai.New` doc; ~43KB spec always in system; cache fields logged but hits unverified | **High on input-side latency/cost** if prefix cache misses | `staticSystemPromptBlock`; `Generate` system blocks |
| **P1** | `grep_theme` sequential ≤501 FlowPOS HTTP / call | `execGrepTheme` loop; caps 500/200 | **High when model greps** | `themebuild/tool_exec.go` |
| **P1** | No generation-scoped theme file cache; duplicate ListFiles/pages/defaults before model | `buildThemeContext` + `buildSnapshotBase` both list/read | **Medium** fixed tax every turn + large if tools re-read | `themebuild/service.go` |
| **P1** | Retry multiplication up to 5 full Generations | `maxThemeCheckRetries=2` in both stages | **High on failing proposals** | `themebuild/proposal.go` |
| **P2** | `validate_changes` can add heavy in-loop checks (up to 4) without thrash detection | Not in `explorationToolNames`; materialize+Check | **Medium** | `tools.go`, `tool_exec.go` |
| **P2** | Stream accumulate retries add up to ~10s+restream, folded into model timing | `streamAccumulateMaxAttempts`, 5s delay | **Low–medium** intermittent | `ai/generator.go` |
| **P2** | Chat queue serialization | One running generation per chat | **High only if stacked prompts** | `DequeueNext` / `ErrGenerationInProgress` |
| **P2** | UX quiet gap after `started` before tools/thinking | No events during context/snapshot build | **Perceived** latency | `doGenerate` vs `useGenerationStream` |
| **P3** | Multi-replica without Redis drops live progress | `server.New` warning | **Perceived hang** | `eventbus` / Redis optional |
| **P3** | Lost bearer after pod restart → fail queued work | `pendingTokens` in-memory | Correctness, not speed | `runOneQueuedGeneration` |

**Classification mapping:** Primary suspected bottleneck class for “DeepSeek feels slow” under default config is **A (model latency)** amplified by **serial tool-loop architecture**, with **B** and **C** as conditional amplifiers. Code does **not** support blaming DeepSeek’s network client setup alone.

---

## Measurements Still Required

Existing logs already support most of the split. Collect **20–50** DeepSeek generations.

### Already available (parse these)

```bash
# Per generation total (merchant-visible)
# slog: "ai: generation wall-clock"  fields: chat_id, mode, elapsed_ms, has_changes

# Per Generate() summary
# "ai: generate call finished"
#   iterations_used, elapsed_ms, model_elapsed_ms, tool_elapsed_ms,
#   total_input_tokens, total_output_tokens, total_reasoning_tokens, reasoning_tokens_reported

# Per tool-loop iteration
# "ai: model call timing"
#   iteration, elapsed_ms, attempts_used, forcing_propose,
#   input_tokens, output_tokens, cache_read_input_tokens, cache_creation_input_tokens,
#   reasoning_tokens, reasoning_tokens_reported, text_block_count, text_chars, tool_use_count

# Per tool
# "ai: tool exec timing"  tool, elapsed_ms, error

# Thrash
# "ai: tool-loop iteration spent unusually many output tokens on exploration only"

# Retries
# "generateValidProposal succeeded" attempts_used
# "checkAndRepair succeeded" attempts_used
# "repair generation completed" elapsed, tokens
# "validate_changes called" call_index, error_count

# Startup proof of DeepSeek config
# "ai: generator configured" model, effort, max_tokens, base_url_set, adaptive_thinking_supported
```

**Example log query sketch** (adjust to your log stack):

```text
"ai: generation wall-clock" OR "ai: generate call finished" OR "ai: model call timing" OR "ai: tool exec timing"
```

**Derived metrics:**

| Need | How |
|---|---|
| total generation time | `generation wall-clock.elapsed_ms` |
| model vs tool | `model_elapsed_ms` / `tool_elapsed_ms` |
| iterations / model calls | `iterations_used` (+ sum of stream `attempts_used`) |
| reasoning / output / input | fields above |
| cache hit rate | fraction iterations with `cache_read_input_tokens > 0` |
| grep cost | filter `tool=grep_theme` on tool exec timing |
| retries | `attempts_used` on proposal/repair logs |

### Missing instrumentation (add later — do not add in this audit)

| Gap | Why it matters |
|---|---|
| **Queue wait ms** | `QueuedAt` exists; no log of `dequeue_time - queued_at` |
| **TTFT / first-token latency** | Only full iteration `elapsed_ms` |
| **Pre-model phase timers** | No timing for `buildThemeContext`, `buildSnapshotBase`, URL fetch separately |
| **FlowPOS per-request latency + count** | No counter of ReadFile/ListFiles per generation |
| **`grep_theme` files_scanned / http_gets** | Cannot see 10 vs 500 from logs today |
| **Provider retry wait ms** | Folded into model elapsed |
| **DeepSeek vs Anthropic A/B tags** | Only startup `base_url_set`; stamp `provider=deepseek` on every timing log |
| **Frontend: time-to-first-stream-event** | Client-side RUM not in backend |

### Decision rule after 20–50 samples

- If median `model_elapsed_ms / wall-clock > ~0.7` → prioritize **effort / thinking / iteration caps / thrash control** (A).  
- If median `tool_elapsed_ms` high and `grep_theme` dominates → prioritize **grep/FlowPOS** (B).  
- If `attempts_used > 1` often → prioritize **retry/themecheck** (C).  
- If `cache_read_input_tokens` always 0 on DeepSeek → prioritize **prefix stability / prompt size** (D).

---

## Recommended Fix Order

**(Investigate/fix order only — no implementation in this audit.)**

1. **Measure first** — confirm A vs B vs C with the log fields above on real DeepSeek traffic (do not skip this).  
2. **If A dominates:** treat `AI_EFFORT=xhigh` + adaptive thinking + iteration thrash as the primary lever; verify DeepSeek effort semantics against measured `reasoning_tokens`.  
3. **Hardening against thrash / runaway exploration:** thrash is currently diagnostic-only; forcing propose/clarify earlier is the highest-leverage *behavioral* control if logs show thrash.  
4. **If B shows up:** `grep_theme` parallelism + generation-scoped ReadFile cache + dedupe `buildThemeContext`/`buildSnapshotBase`.  
5. **DeepSeek-specific:** verify cache_read tokens; keep history summary cache on; investigate zero-tool nudge rate.  
6. **If C shows up:** narrow repair Generate (lower effort / fewer iterations) and expand autofix.  
7. **UX:** emit progress during pre-model FlowPOS/context build so “started → long silence” is not mistaken for a hung request.  
8. **Ops:** Redis on multi-replica; ensure `ai: generator configured` shows expected DeepSeek model/effort at boot.

---

*End of audit. No code was modified.*

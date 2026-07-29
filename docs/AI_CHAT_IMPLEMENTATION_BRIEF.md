# ai-chat — implementation brief (phases 1–3)

Paste this at the start of a Claude Code session in the `ai-chat` repo. Work
one phase at a time. Do not start a phase until the previous one's tests pass.

---

## Context you must read first

Before writing any code, read these files. Do not skip this step — the
conventions in them are not guessable:

- `backend/internal/ai/prompts/theme_engine_spec.md` — the theme convention
  you are enforcing. §1 (tags/filters), §3 (page boilerplate), §5
  (`pages.json`), §7 (data model), §8 (components), §12 (hard rules).
- `backend/internal/themefs/pathsafety.go` and `pathsafety_test.go` — the
  style all new pure-function packages must match.
- `backend/internal/themefs/layout.go`, `pages.go`, `disk.go`
- `backend/internal/ai/generator.go` — the current one-shot call you are
  replacing in phase 2.
- `backend/internal/modules/themebuild/service.go` — `doGenerate`,
  `validateProposal`, `buildWritePlan`, `commitWritePlan`,
  `buildEditingFilesContext`, `generationTracker`.
- `README.md` — note the "Known simplifications" section. Some of it is now
  out of date relative to the code; trust the code.

## Standing rules for this whole brief

1. Follow the existing architecture: `handlers` → `service` → `repository` →
   `model`. Handlers never touch SQL.
2. New pure logic goes in its own `internal/` package with table-driven tests,
   in the style of `themefs/pathsafety_test.go`. No DB, no network, no disk in
   those packages.
3. Do not add a dependency without saying why first. `go-git` in phase 4 is
   pre-approved; nothing else is.
4. Do not refactor code the phase doesn't require. No drive-by cleanups.
5. Comments explain non-obvious constraints only. No comments narrating what a
   line does.
6. Every phase ends with `make test` and `make lint` passing.
7. If a requirement here conflicts with the spec or the existing code, stop and
   say so instead of guessing.

---

# Phase 1 — `internal/themecheck`

## Goal

Nothing gets written to a theme unless it passes the spec's hard rules. When it
fails, the model gets the failures back and retries.

## Deliverable

New package `backend/internal/themecheck/`. Pure functions only.

```go
type Severity string // "error" | "warning"

type Finding struct {
    Path     string   // theme-relative, or "" for theme-wide findings
    Rule     string   // stable id, e.g. "page-boilerplate"
    Severity Severity
    Message  string   // written FOR THE MODEL to act on: say what is wrong
                      // and what the correct form is
}

type Snapshot struct {
    Files      map[string]string // theme-relative path -> content
    LayoutStart string
    LayoutEnd   string
    PagesJSON   string
}

func Check(proposal Proposal, snap Snapshot) []Finding
```

`Proposal` mirrors `ai.Result` — don't duplicate the type, accept an interface
or a small struct the caller maps into.

## Rules to implement, in this order

Each rule is its own unexported function plus its own test table. Implement and
test one before starting the next.

| # | Rule id | Severity | What it checks |
|---|---|---|---|
| 1 | `page-boilerplate` | error | Every `pages/**/*.liquid` opens with the exact §3 `layout-start` render (all 9 params) and closes with the `layout-end` render. Params may not be added, removed, or reordered. |
| 2 | `allowed-syntax` | error | Only §1 tags and filters appear. Explicitly reject `{% schema %}`, `{% section %}`, `{% include %}`, `{% javascript %}`, `{% stylesheet %}`, and any filter not in §1. |
| 3 | `balanced-tags` | error | Every `if`/`for`/`capture`/`comment` has its matching close, correctly nested. |
| 4 | `render-target-exists` | error | Every `{% render 'x' %}` path starts with `liquid/` or `components/` and resolves to a file that exists in `snap.Files` or in the proposal itself. |
| 5 | `asset-registered` | error | Every new `pages/css/*.css` or `components/css/*.css` has a `<link>` in `layout-start`; every new `js/*.js` has a `<script defer>` in `layout-end`. JS must sit in the §10 order, after `storefront-api.js` if it calls the API client. |
| 6 | `page-route` | error | A new `pages/<slug>.liquid` has a `pages.json` entry where `page` == basename and `slug` == basename. `path` is `/pages/auth` for files under `pages/auth/`, `/pages` otherwise. Slug not already taken. No second entry of a system `type`. |
| 7 | `seo-filled` | warning | `seo_title`, `seo_description`, `seo_keywords` non-empty and not placeholder text ("...", "TODO", "Lorem"). |
| 8 | `theme-token` | error | No raw hex/rgb colour where a `--theme-*` key exists in `defaults.json`. Every `var(--theme-*)` / `var(--layout-*)` has a fallback. |
| 9 | `bool-guard` | error | Bare `{% if x %}` on a field the spec marks bool-ish (`menu.items[].active`, `product.on_sale`, `has_choices`, `has_variants`, `is_available`, `customer_authenticated`) must be `{% if x == true or x == 1 %}`. |
| 10 | `no-framework` | error | No Tailwind/Bootstrap classes, no React/Vue/jQuery, no `<script src>` pointing off-theme, no build-tool config. |
| 11 | `js-shape` | warning | Each new `js/*.js` is IIFE-wrapped, queries its root element and returns early if absent, and uses `data-*` hooks rather than class selectors. |
| 12 | `known-fields` | error | **Implement last, most important.** Extract every `object.field` reference from Liquid output and conditions. Check each against the §7 data model table. Anything not listed is an invented field. Encode §7 as a Go map in this package; do not parse the markdown at runtime. |

## Wiring

In `themebuild.doGenerate`, after the existing `validateProposal`:

- Build a `Snapshot` from `themefs.Store`.
- Call `themecheck.Check`.
- Any `error` findings — do NOT reach `buildWritePlan`.
- Send findings back to the model as a new turn and retry. **Max 2 retries.**
  Then fail the generation with a merchant-friendly message.
- `warning` findings — write the files, but return them so the UI can show
  them.
- Record attempts and findings so retry frequency is measurable later.

## Done when

`make test` passes, and a hand-written bad proposal (invented field + missing
CSS link + bare bool guard) produces exactly three error findings and is never
written to disk.

---

# Phase 2 — tool loop replacing the one-shot call

## Goal

The model reads the theme before changing it. Today it cannot, so it overwrites
files it has never seen.

## Tools to define

Read-only tools, all going through `themefs.Store`, every path passed through
`ValidateGeneratedFilePath` first:

- `list_theme_files` — no args, returns the theme file tree.
- `read_theme_file` — `{paths: string[]}`, max 10 per call, returns contents.
  Cap total returned bytes and say so in the result when truncated.
- `grep_theme` — `{pattern: string, path_glob?: string}`, returns matching
  file paths with matching line numbers and lines. Plain substring or simple
  regex, your call — document which.

Plus the terminal tool:

- `propose_changes` — the existing `resultSchema`, unchanged, converted from a
  structured-output format into a tool definition.

## Changes to `internal/ai/generator.go`

- Replace the single `Messages.NewStreaming` call with a loop: call → if the
  response contains `tool_use` for a read tool, execute it, append the
  `tool_result`, call again.
- Terminate when the model calls `propose_changes`. Return its input as the
  `Result`.
- Cap at 8 iterations. If the cap is hit without `propose_changes`, return an
  error naming the cap.
- Keep the 1h `cache_control` breakpoint on the spec system block exactly as
  it is now.
- Add to the system prompt: "Before you modify any existing file, read it with
  `read_theme_file`. Never write a file you have not read. Emit only files
  whose content actually changes."
- Add a mode line to the dynamic system block: `MODE: edit | brand | copy |
  pages`. In `brand` mode, only `propose_changes` is offered and only
  `defaults.json` may be written.

## Changes to `themebuild`

- Delete `buildEditingFilesContext`, `maxEditingFiles`, and
  `editingFilesBlock`. The model fetches files itself now.
- `buildThemeContext` keeps `pages.json` and `defaults.json` and additionally
  supplies the file tree.
- Pass a tool-executor closure into `ai.Generate` so `ai` never touches
  `themefs` directly.

## Done when

Given a theme with a customised `components/testimonials.liquid`, the prompt
"make testimonials light with a cream background on the about page" causes the
model to read that file and change only the render call in
`pages/about.liquid`, leaving the component untouched.

---

# Phase 3 — WebSockets and durable generation state

## Goal

The merchant sees live progress during a 2-minute run, and progress survives a
pod restart or a second replica.

**Do the state work first.** WebSockets without it will silently fail the
moment you run more than one instance.

## 3a — durable generation state

New migration, table `generations`:

| column | notes |
|---|---|
| `id` | |
| `chat_id` | unique where status = 'running' |
| `tenant_id` | |
| `status` | `running` / `succeeded` / `failed` |
| `error` | nullable |
| `attempts` | themecheck retry count |
| `started_at`, `finished_at` | |

- Replace the in-memory `generationTracker` with this table. Keep the same
  method signatures so callers don't change.
- Add a reaper: on startup and every minute, mark `running` rows older than
  `generateTimeout` as `failed` with a timeout message.
- Replace the in-memory rate limiter with a Redis token bucket, or leave a
  clearly-marked TODO if Redis isn't available yet — but the `generations`
  table is not optional.

## 3b — event log

New table `generation_events`: `generation_id`, `seq` (monotonic per
generation), `type`, `payload JSON`, `created_at`. Keep the last 200 per chat.

Event types the tool loop emits:

| type | payload |
|---|---|
| `started` | |
| `reading` | `{paths: []}` |
| `searching` | `{pattern}` |
| `thinking` | `{text}` — streamed summary deltas |
| `proposed` | `{file_count, paths: []}` |
| `checking` | |
| `check_failed` | `{findings: [], attempt}` |
| `repairing` | `{attempt}` |
| `written` | `{paths: []}` |
| `done` | `{summary}` |
| `failed` | `{message}` |

Every event is written to the table AND published to a Redis channel
`gen:{chat_id}`.

## 3c — the socket

- `GET /api/v1/chats/:chatId/stream` — upgrade to WebSocket. Auth via the same
  bearer token; verify tenant ownership of the chat before upgrading.
- Server → client only. The client never sends commands; prompts still go over
  `POST /chats/messages`.
- On connect the client may send `{"last_seq": N}` once. Replay events after
  `N` from `generation_events`, then subscribe to the Redis channel.
- Ping every 30 seconds. Close cleanly on `done` or `failed`.
- Use `github.com/coder/websocket` (or `gorilla/websocket`) — say which and
  why before adding it.
- **Do not remove `GET /chat`.** It stays as the polling fallback for clients
  whose socket won't connect.

## Done when

Two server instances are running, a generation started on instance A streams
its events to a socket connected to instance B, and killing instance A
mid-generation causes the reaper to mark it failed within a minute rather than
leaving the merchant waiting forever.

---

## What is deliberately NOT in this brief

Do not build these yet, and do not stub them:

- git-backed drafts and revert (phase 4)
- fixture render preview (phase 5)
- generated theme manifest with hash caching (phase 6)
- base theme scaffold and from-scratch creation (phase 7)
- eval harness (phase 8)

If a phase above seems to need one of these, stop and say which, rather than
building a partial version of it.

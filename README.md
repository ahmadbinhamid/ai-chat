# ai-chat

Standalone Go backend for the AI theme-builder chat feature: a merchant
describes a page or section in chat, Claude generates the Liquid theme files
implementing it (following `THEME_ENGINE_SPEC.md`'s convention — see
`backend/internal/ai/prompts/theme_engine_spec.md`), and the merchant applies
the change to their live theme with one click.

This is **not** a FlowPOS marketplace mini-app — there is no install/
uninstall/webhook lifecycle, no app-embed handshake, and no frontend of its
own. It's a plain multi-tenant API consumed by the tenant dashboard, scaffolded
from the same tooling as the `app-booking`/`appointments`/`quotes` apps
(Go + Gin + `database/sql` + MySQL, JWT auth) with the app-lifecycle parts
removed.

## Layout

```
ai-chat/
├── package.json                  npm run dev/build/migrate/test — thin wrapper around Go, see below
│
└── backend/
    ├── go.mod / go.sum
    ├── Makefile                   run / build / migrate / fresh / create / tidy / test / lint
    ├── .air.toml                  live-reload config for `air` (also watches the embedded prompt .md)
    ├── .env.example
    ├── .golangci.yml
    ├── cmd/
    │   ├── server/                HTTP API entrypoint — config/DB wiring + graceful shutdown
    │   └── migration/             migration CLI (create / run / fresh)
    ├── internal/
    │   ├── config/                env -> Config (fails fast on a missing JWT_SECRET/THEME_STORAGE_ROOT — no insecure default)
    │   ├── db/                    pooled MySQL connection
    │   ├── logging/                structured (slog/JSON) request logging with a correlation ID
    │   ├── auth/                  JWT verify/mint (golang-jwt/jwt) — tenant_id/user_id claims, no local user table
    │   ├── httpresponse/           shared {"data"/"error"/"meta"} response helpers
    │   ├── ratelimit/              per-tenant token-bucket limiter (generation endpoint only)
    │   ├── ai/                    Claude client: builds the system prompt, calls the API with
    │   │                          Structured Outputs, parses the typed result
    │   │   └── prompts/theme_engine_spec.md   embedded via //go:embed — the canonical theme convention doc
    │   ├── themefs/                theme-filesystem boundary: path-safety validation, the pages.json
    │   │                          merge, the layout <link>/<script> splice (all pure, unit-tested),
    │   │                          plus the actual disk I/O
    │   ├── modules/
    │   │   ├── chat/               model/repository/service — the conversation aggregate (Chat + Message)
    │   │   └── themebuild/         model/repository/service — Generate (call Claude, persist proposed
    │   │                          changes) and Apply (write them to the real theme filesystem)
    │   └── server/
    │       ├── server.go           router + dependency wiring
    │       └── handlers/           HTTP handlers, auth middleware, error mapping
    └── database/
        ├── migrations/             timestamped Go migration files (see Migrations below)
        └── migrator/                the migration engine (registry, run, fresh)
```

**On the "controller / routes / models" naming you'll see in other stacks:**
Go doesn't use MVC terminology — the idiomatic equivalents used throughout
this codebase are **handler** (= controller; `internal/server/handlers/`),
**route registration** (= routes; the `r.GET(...)`/`r.POST(...)` calls in
`internal/server/server.go`), and **model** (`model.go` in each
`internal/modules/<name>/` package). Each module additionally splits
persistence (`repository.go`, raw parameterized SQL) from business logic
(`service.go`) — handlers never touch SQL directly.

Dependencies flow inward: `cmd` → `server` → `modules/<feature>`
(`handlers` → `service` → `repository` → `model`). Only `cmd` and
`server.go` wire concrete implementations together.

## Running (no Docker)

```bash
cp backend/.env.example backend/.env   # fill in JWT_SECRET, THEME_STORAGE_ROOT, ANTHROPIC_API_KEY, DB creds
npm install                            # only pulls in `concurrently`-free tooling; there is no JS runtime dependency
cd backend && go mod tidy && cd ..
npm run migrate                        # apply the schema
npm run dev                            # go run ./backend/cmd/server (or `air`, if installed, for live reload)
```

`npm run dev` exists purely so this project starts the same way as every
other app in the monorepo, even though it has no frontend and no real npm
dependencies — see the root `package.json`. If you prefer working directly
in Go: `cd backend && make run` (or `air`).

Other scripts: `npm run build`, `npm run migrate:fresh` (drop + re-run every
migration), `npm test`, `npm run lint` (requires `golangci-lint` installed
separately).

Getting a dev auth token (`JWT_DEV_TOKENS=true` in `.env`, the default):

```bash
curl -X POST http://localhost:8080/api/v1/dev/token \
  -H "Content-Type: application/json" \
  -d '{"tenant_id": 1, "user_id": 1, "user_email": "dev@example.com"}'
```

## Migrations

Plain Go files in `backend/database/migrations` — each registers itself with
the engine in `backend/database/migrator` via `init()`, exposing `Up`/`Down`
functions over a `*sql.DB`. The engine tracks applied migrations in a
`migrations` table and applies them oldest-first by filename timestamp. The
server itself never auto-migrates.

```bash
cd backend
make create name=add_something   # scaffolds 2026..._add_something.go
make migrate                     # or: make fresh, to drop everything and re-run from scratch
```

## Database schema

| Table | Purpose |
|---|---|
| `chats` | One conversation thread — what the builder UI's sidebar lists (title, recency). `tenant_id`/`user_id` carry no local FK — identity lives in the JWT, not a local users table. `theme_slug` is just a string the caller supplies. |
| `chat_messages` | Append-only turn log (no `updated_at` — a turn is never mutated after the fact). `role='user'` rows always have a `user_id` (enforced by a `CHECK` constraint); assistant/system rows don't. `apply_status`/`applied_at` track the single "Apply to theme" action the UI exposes per assistant turn. |
| `chat_generated_files` | One file artifact an assistant turn proposed, with a `previous_content` snapshot captured at proposal time (so Apply never has to re-read possibly-drifted disk state). |
| `chat_apply_actions` | Non-file side effects a turn proposed — merging a `pages.json` entry, or splicing a `<link>`/`<script>` into the layout files. Polymorphic (`action_type` + `payload JSON`) rather than one table per kind, since `pages.json` and the layout files are shared across every chat for a theme and can't be modeled as a plain file overwrite without risking one chat clobbering another's changes. |

See the ai-chat ERD discussion (or re-derive from the migration files
directly) for the full column list; the migrations are the source of truth.

## API

All routes are JSON. Everything under `/api/v1` except `/dev/token` requires
`Authorization: Bearer <jwt>` (claims: `tenant_id`, `user_id`, `user_email`).

| Method | Path | Description |
|---|---|---|
| GET | `/health` | Liveness + DB connectivity check. |
| POST | `/api/v1/dev/token` | Dev-only JWT mint. Disabled unless `JWT_DEV_TOKENS=true`. |
| GET | `/api/v1/chats` | List the caller's chats, most recent first. `?theme_slug=` to filter, `?page=&limit=` to paginate. |
| GET | `/api/v1/chats/:chatId` | A chat plus its full message transcript. |
| PATCH | `/api/v1/chats/:chatId` | Rename a chat (`{"title": "..."}`). |
| POST | `/api/v1/chats/messages` | Send a prompt. `{"prompt": "..."}` alone starts a new chat (requires `theme_slug` too); add `"chat_id"` to continue an existing thread. Rate-limited per tenant (`GENERATION_RATE_LIMIT_PER_MINUTE`) — the only route that calls Claude. |
| POST | `/api/v1/chats/:chatId/messages/:messageId/apply` | Write that message's pending file changes + side effects to the real theme filesystem. Safe to retry after a partial failure — already-applied rows are skipped. |

## Known simplifications (not oversights — read before extending)

- **No ownership-preloading middleware.** `app-booking`'s
  `RequireLocationOwnership`-style middleware (load the resource once,
  reuse it down the handler chain) is a good pattern, but with only one
  nested resource level (`/chats/:chatId/...`) here, every service method
  just checks `tenant_id` itself on each call instead. Worth extracting into
  middleware if more nested routes get added later.
- **The rate limiter is in-process, single-replica only** (an in-memory
  `map[tenantID]*rate.Limiter`) — same caveat the sibling apps' background
  sync schedulers already carry. Fine for one instance; swap for a shared
  store (Redis) before running more than one.
- **Apply is not transactional across the DB and the filesystem** — there's
  no distributed transaction spanning a MySQL row update and a disk write.
  A failure mid-`Apply` leaves already-written files/actions marked
  `applied` and the rest `pending`; retrying only touches what's still
  `pending`, so it's safe, just not atomic.
- **`internal/ai/prompts/theme_engine_spec.md` is a point-in-time copy** of
  the canonical spec (currently maintained alongside the Liquid theme
  reference implementation in `flowpos-backend`). It's embedded at compile
  time (`//go:embed`), so this service is self-contained, but it will drift
  if the canonical spec changes and this copy isn't updated to match —
  there's no automated sync between the two repos.
- **How this service reaches the theme filesystem is assumed to be a shared
  mount** (`THEME_STORAGE_ROOT` pointing at the same disk `flowpos-backend`
  writes theme files to). If this service and `flowpos-backend` ever run on
  different hosts, `internal/themefs.Store` needs to become an HTTP client
  against a `flowpos-backend`-side file API instead of `os.ReadFile`/
  `os.WriteFile` — flagged here rather than guessed at.

# ai-chat

Go backend for the AI theme-builder chat feature: a merchant describes a page
or section in chat, DeepSeek generates the Liquid theme files implementing it,
and the merchant applies the change to their live theme with one click.

## Setup

**1. Prerequisites**

```bash
brew install go mysql
brew services start mysql
```

Optional but recommended:

```bash
go install github.com/air-verse/air@latest      # live-reload for `make run`
brew install golangci-lint                      # for `make lint`
```

**2. Create the database**

```bash
mysql -u root -p -e "CREATE DATABASE ai_chat CHARACTER SET utf8mb4;"
```

**3. Configure environment**

```bash
cp backend/.env.example backend/.env
```

Fill in `backend/.env`. The only values that are actually required to start
the server are `FLOWPOS_API_BASE` (auth is fully delegated to FlowPOS — every
request forwards its bearer token there, there is no local auth system) and
`AI_API_KEY`. Everything else in `.env.example` has a working default for
local development.

In production, set `APP_ENV=production` and `AI_MODELS_CONFIG` (e.g.
`config/ai-models.json`): with `APP_ENV=production` the server refuses to
start without a catalogue file instead of falling back to the one-model `AI_*`
setup, whose base URL defaults to `api.deepseek.com`. `AI_CHAT_FAKE_MODE` is
exempt. Production also needs `REDIS_URL`: theme writes are locked through
Redis so replicas can't write the same theme at once. Without it the server
refuses to start unless you set `AI_CHAT_SINGLE_REPLICA=true` to declare that
exactly one replica will ever run. On startup the server logs which theme lock
backend is active, and the catalogue it loaded (source, providers
and base URLs, default and vision model, model count; never keys).
Leave `REDIS_URL` empty unless
you're actually running Redis locally — it only backs cross-replica event
delivery and isn't needed for a single instance.

**Using our DeepSeek key directly**

DeepSeek's own servers aren't an OpenRouter host for V4, so to use our DeepSeek
balance for DeepSeek Pro and Flash, point the catalogue at the BYOK file:

```env
DEEPSEEK_API_KEY=sk-...
AI_MODELS_CONFIG=config/ai-models.byok.json
```

Grok, Kimi, Gemini and DeepSeek Flash Vision stay on OpenRouter (`AI_API_KEY`).
To go back, set `AI_MODELS_CONFIG=config/ai-models.json` and restart. While on
the direct key there is **no failover to OpenRouter**: if DeepSeek's API is
down or the balance runs out, DeepSeek turns fail until you switch back.

**4. Install dependencies and migrate**

```bash
cd backend
go mod tidy
make migrate
```

**5. Run it**

```bash
make run
```

Check it's up: `curl localhost:8080/health`. It returns `200` with the build
info, or `503 {"status":"unhealthy"}` when the database is unreachable (the
real error goes to the server log only).

## Commands

Run from `backend/`:

| Command | Purpose |
|---|---|
| `make run` | Start the server — uses `air` for live-reload if installed, else plain `go run`. |
| `make build` | Compile to `./tmp/main`. |
| `make migrate` | Apply any pending migrations. |
| `make fresh` | Drop every table and re-run all migrations from scratch. |
| `make create name=x` | Scaffold a new migration file (`database/migrations/<timestamp>_x.go`). |
| `make tidy` | Sync `go.mod`/`go.sum` with actual imports. |
| `make test` | `go test ./...` |
| `make lint` | `golangci-lint run ./...` (requires `golangci-lint` installed). |
| `make eval` | Run the fixed task list in `internal/evals` against the real DeepSeek + FlowPOS pipeline. Needs `EVAL_BEARER_TOKEN` and `EVAL_TENANT_ID` from a real logged-in tenant user — see `cmd/eval/main.go`'s doc comment. |

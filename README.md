# ai-chat

Go backend for the AI theme-builder chat feature: a merchant describes a page
or section in chat, Claude generates the Liquid theme files implementing it,
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
`AI_PROVIDER` (`anthropic` or `deepseek`, must be one of those two). Everything
else in `.env.example` has a working default. Leave `REDIS_URL` empty unless
you're actually running Redis locally — it only backs cross-replica event
delivery and isn't needed for a single instance.

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

Check it's up: `curl localhost:8080/health`

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
| `make eval` | Run the fixed task list in `internal/evals` against the real Claude + FlowPOS pipeline. Needs `EVAL_BEARER_TOKEN` and `EVAL_TENANT_ID` from a real logged-in tenant user — see `cmd/eval/main.go`'s doc comment. |

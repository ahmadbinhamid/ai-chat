# Builder execution examples (ML-9 / ML-12 / ML-13)

Compact, tenant-scoped builder execution records for future local-model
evaluation and training. **Collection is OFF by default.**

## Why the table was missing (ML-12)

The migration file existed and was registered:

`database/migrations/20260918000001_create_builder_execution_examples.go`

The server **never auto-migrates**. Until `make migrate` / `go run ./cmd/migration run`
was executed against the target DB, the table did not exist.

## Safe workflow (ML-13 collection campaign)

See [COLLECTION_CHECKLIST.md](./COLLECTION_CHECKLIST.md).

```bash
# 1) Apply schema (once per environment)
cd backend && make migrate

# 2) Development / staging ONLY — enable collection for the server process
export BUILDER_TRAINING_DATA_ENABLED=true
export BUILDER_TRAINING_RETENTION_DAYS=30
export BUILDER_TRAINING_MAX_RECORDS=10000
# leave BUILDER_TRAINING_STORE_SANITIZED_PROMPT=false unless approved

# 3) Run real builder generations (doGenerate → Collector → MySQL)
#    Use the manual checklist — do not fabricate JSONL rows.

# 4) Export (tenant-scoped; strips chat/generation IDs; fingerprint only by default)
go run ./cmd/builderexamples export -tenant <TID> -out examples.jsonl

# 5) Readiness (no raw prompts)
go run ./cmd/builderexamples readiness -tenant <TID>

# 6) Transform + snapshot (does NOT train)
go run ./cmd/builderdataset snapshot -in examples.jsonl -out ./builder-semantic-v1
go run ./cmd/builderdataset status -dataset ./builder-semantic-v1
```

### Production

Keep `BUILDER_TRAINING_DATA_ENABLED=false` (default in `.env.example`) until
explicitly approved.

### Controlled smoke (no DeepSeek)

```bash
go run ./cmd/builderexamples smoke -tenant 9001
go run ./cmd/builderexamples status -tenant 9001
```

## Retention

| Env | Default |
|-----|---------|
| `BUILDER_TRAINING_RETENTION_DAYS` | 30 |
| `BUILDER_TRAINING_MAX_RECORDS` | 10000 |

Trim runs after each successful insert. No unbounded in-memory cache in the SQL path.

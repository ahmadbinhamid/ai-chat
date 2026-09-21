# Builder Training Data Collection Campaign (ML-13)

**Do not train models in this phase.**  
**Do not fabricate training rows.**  
**Production default remains `BUILDER_TRAINING_DATA_ENABLED=false`.**

## Dev / staging workflow

```bash
cd backend
make migrate   # ensure builder_execution_examples exists

# Enable ONLY in the local/staging process:
export BUILDER_TRAINING_DATA_ENABLED=true
export BUILDER_TRAINING_RETENTION_DAYS=30
export BUILDER_TRAINING_MAX_RECORDS=10000
# leave BUILDER_TRAINING_STORE_SANITIZED_PROMPT=false unless explicitly approved

# Run the API and perform REAL builder requests (checklist below).

# Tenant-scoped export (no raw prompts by default; chat/generation IDs stripped):
go run ./cmd/builderexamples export -tenant <TID> -out examples.jsonl

# Readiness against stored rows for one tenant:
go run ./cmd/builderexamples readiness -tenant <TID>

# Transform + snapshot (still does NOT train):
go run ./cmd/builderdataset snapshot -in examples.jsonl -out ./builder-semantic-v1

# Gate text:
go run ./cmd/builderdataset status -in examples.jsonl
# or against splits:
go run ./cmd/builderdataset status -dataset ./builder-semantic-v1
```

Operator-only (local DB access): `go run ./cmd/builderexamples status -global` and  
`go run ./cmd/builderexamples readiness -global` show **counts / readiness only** — never another tenant's prompts.

## Training gate (unchanged)

| Metric | Minimum |
|--------|---------|
| usable_total | 500 |
| positive_semantic | 200 |
| distinct_semantic_groups | 100 |
| protected_fields | 50 |
| preferences | 50 |
| clarification | 30 |
| seo_related | 40 |
| compound | 40 |

Do **not** lower these to unlock training.

## Manual collection checklist

Run each item as a **real** builder request in a staging/dev theme.  
Check the box only after a row appears for your tenant (`builderexamples readiness`).

### CONTENT
- [ ] rewrite a blog for a software company
- [ ] make this page more professional
- [ ] make the content suitable for SaaS customers

### SEO
- [ ] change meta title only
- [ ] update description but preserve slug
- [ ] preserve existing meta title / JPRO meta titles

### PAGE
- [ ] create one page
- [ ] create two pages
- [ ] create page plus navigation

### REGISTRY
- [ ] register existing page
- [ ] unregister page (if supported)
- [ ] verify existing registration

### STYLE
- [ ] change button color
- [ ] change heading style
- [ ] modify spacing

### COMPOUND
- [ ] update blog content + SEO
- [ ] create page + navigation
- [ ] modify content + preserve selected fields

### CLARIFICATION (expect ambiguous / clarify outcomes)
- [ ] make the site better
- [ ] improve this
- [ ] make it modern

### FAILURE / EDGE (keep labeled; do not train on these automatically)
- [ ] cancelled generation
- [ ] provider timeout (if reproducible safely)
- [ ] validation failure
- [ ] partial success

Aim for **diversity**, not only row count. Repeated near-identical prompts inflate `stored_total` but not unique semantic groups — the readiness report tracks both.

## Privacy

- Default: fingerprint only (`BUILDER_TRAINING_STORE_SANITIZED_PROMPT=false`)
- No secrets/credentials in payloads (collector sanitization + dataset exclusions)
- Retention: 30 days / 10 000 max rows

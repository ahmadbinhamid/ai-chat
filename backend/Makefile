.PHONY: run build migrate fresh create tidy test lint eval

# Start the HTTP API server (live reload via `air` if installed — see
# .air.toml; falls back to a plain `go run` if air isn't on PATH).
run:
	@command -v air >/dev/null 2>&1 && air || go run ./cmd/server

# Compile everything (syntax/typecheck across the module).
build:
	go build -o ./tmp/main ./cmd/server

# Apply any migrations that haven't run yet.
migrate:
	go run ./cmd/migration run

# Drop all tables and re-run every migration from scratch.
fresh:
	go run ./cmd/migration fresh

# Generate a new migration file. Usage: make create name=create_widgets
create:
	@go run ./cmd/migration create $(or $(name),$(filter-out create,$(MAKECMDGOALS)))

# Tidy module dependencies.
tidy:
	go mod tidy

# Run the test suite.
test:
	go test ./...

# Static analysis — requires golangci-lint (https://golangci-lint.run/welcome/install/).
lint:
	golangci-lint run ./...

# Run the fixed eval task list against the real pipeline (Claude + flowpos-backend + DB).
# Requires EVAL_BEARER_TOKEN and EVAL_TENANT_ID (a real, already logged-in test tenant
# user) — see cmd/eval/main.go's package doc comment.
eval:
	go run ./cmd/eval

# Catch-all so a positional migration name (`make create foo`) isn't treated
# as its own target.
%:
	@:

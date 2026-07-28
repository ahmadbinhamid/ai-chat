.PHONY: run build migrate fresh create tidy test lint

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

# Catch-all so a positional migration name (`make create foo`) isn't treated
# as its own target.
%:
	@:

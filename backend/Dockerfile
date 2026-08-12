# syntax=docker/dockerfile:1

# ---- build stage ----
# Pin to the same minor version go.mod declares (see the CI workflow's own
# "read version from go.mod" step) — an image tag is the Docker-side
# equivalent of that pin, kept in sync by hand since Dockerfile has no
# built-in way to read go.mod itself.
FROM golang:1.26.4-alpine AS build
WORKDIR /src

# Module download cached in its own layer, separate from the source copy
# below — an app-code-only change then never re-triggers `go mod download`,
# which is by far the slowest step here on a cold cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0: go-sql-driver/mysql is a pure-Go driver, no libmysqlclient
# needed — a static binary is what makes the alpine runtime stage below
# trivial (no glibc/musl ABI mismatch between build and runtime base images
# to worry about). -trimpath/-ldflags="-s -w" strip file-system paths and
# debug symbols from the binary; neither is needed at runtime and both
# shrink the final image.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---- runtime stage ----
FROM alpine:3.20 AS runtime

# ca-certificates: this service makes outbound HTTPS calls to Anthropic/
# DeepSeek and flowpos-backend (internal/auth, internal/themefs) — without
# this every one of those fails TLS certificate verification, not just
# looks insecure. curl: needed for HEALTHCHECK below; distroless was
# considered instead of alpine specifically for the smaller/more locked-down
# image, but distroless ships no shell and no curl/wget, which makes a
# curl-based HEALTHCHECK (what flowPOS's other services already use)
# unworkable without vendoring a second, healthcheck-only binary into the
# image — not worth it for one HTTP GET.
RUN apk add --no-cache ca-certificates curl \
    && addgroup -S aichat \
    && adduser -S aichat -G aichat

WORKDIR /app
COPY --from=build /out/server ./server

# Non-root: this process only ever needs to listen on one port and make
# outbound HTTP calls — no reason for it to run as root inside the
# container, and every FlowPOS service should be able to say the same.
USER aichat

ENV PORT=8080
EXPOSE 8080

# Hits the same /health route server.go already mounts (checks the DB
# connection, see New's health handler) — a container that can accept TCP
# connections but can't reach its own database is exactly the "up but not
# actually healthy" case a HEALTHCHECK exists to catch.
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD curl -f "http://127.0.0.1:${PORT}/health" || exit 1

ENTRYPOINT ["./server"]

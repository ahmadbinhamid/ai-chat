# Standing rules

These apply to every session working from `docs/AI_CHAT_IMPLEMENTATION_BRIEF.md`.

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

See `docs/AI_CHAT_IMPLEMENTATION_BRIEF.md` for phase details.

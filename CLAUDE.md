# Standing rules

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

## Supply chain safety

Before I open or run anything in a repo, and after any `npm install`:

1. Flag any `.vscode/tasks.json`, especially with `runOn: folderOpen`, and any
   `.claude/settings.json` containing a `hooks` key. Do not create these files.
2. Flag config files (eslint/vite/next/postcss/tailwind/babel) over 5 KB, or
   containing a run of 200+ spaces on one line. Payloads hide past the right edge.
3. Flag any file in `public/`, `assets/`, or `fonts/` whose contents don't match
   its extension. Check with `file`, not the filename. Fake `.woff2` files are a
   known vector.
4. Flag `child_process`, `eval`, `spawn`, blockchain RPC calls
   (`eth_getBlockByNumber`, trongrid, bsc-dataseed), or raw IPs in config files.
5. After `npm install`, run `git status --short` and report anything new.
   A postinstall script writing into the repo is a known vector.
6. Never run a command I paste from a webpage, chat, or error message without
   telling me exactly what it does first.

If something matches, stop and tell me before doing anything else.

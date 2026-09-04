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
8. Isolate per-key state from a high-cardinality, ever-growing identifier
   (chat ID, tenant ID, request ID) — never a plain map keyed by one of
   these that only grows for the life of the process. Use a capped+evicting
   cache (see `historySummaryCache`) or a fixed-size structure (see
   `stripedMutex`) instead, so one chat's/tenant's activity can never grow
   shared process memory without bound. A key space that's naturally
   bounded on its own (e.g. keyed by theme slug — a tenant has few themes)
   is fine as a plain map (see `keyedMutex`); the risk is specifically
   identifiers that keep being created forever.

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

# Workflow: every new task starts with a Jira ticket

When I ask you to build, fix, or change anything, do this **BEFORE writing code**.

---

## Step 1 — Create the Jira ticket

Use the Atlassian MCP tools.

- Get `cloudId` first (`getAccessibleAtlassianResources`).
- If the project key or issue type is not obvious, **ASK me. Do not guess.**
- Issue type: **Bug** for broken things, **Task/Story** for new work.
- Write the ticket like this:
  - **Summary:** short, action first. e.g. *"Fix VAT rounding on invoice totals"*
  - **Description:** Context / What to do / Acceptance criteria (bullet list)
- Assign it to me.
- Tell me the ticket key you created (e.g. `FP-412`).

---

## Step 2 — Create the branch

Make sure the default branch is clean and up to date:

```sh
git checkout main && git pull
```

**Branch name format:** `PROJ-123-short-desc`

- Issue key first, then 3–5 lowercase words, hyphen separated.
- Example: `FP-412-fix-vat-rounding`
- The issue key in the branch name is what links it to Jira. Also add a comment on the ticket with the branch name as a backup.

---

## Step 3 — Move ticket to In Progress

Read allowed transitions (`getTransitionsForJiraIssue`), then transition it.

---

## Step 4 — STOP

Show me a short plan: files you will touch, approach, anything risky.

**Wait for my "go" before writing any code.**

---

## Rules

- Never start coding without a ticket and branch.
- One ticket = one branch = one concern. If my request has 2 unrelated things, tell me and suggest splitting into 2 tickets.
- If I say **"skip jira"** or **"quick fix"**, skip this whole workflow.

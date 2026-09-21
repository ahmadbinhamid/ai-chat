# AI Builder Page Lifecycle Audit

**Status:** READ-ONLY AUDIT — no code, prompts, ML, or workflow changes in this document’s production of findings.  
**Scope:** FlowPOS theme page architecture + AI Builder (`ai-chat`) interpretation/execution.  
**Date:** 2026-09-19  

---

## 1. Executive Summary

FlowPOS pages are **file + JSON registry**, not DB page rows:

| Concern | Authoritative store |
|---------|---------------------|
| Route / SEO / publish status | Theme-root `pages.json` |
| Page body | `pages/<slug>.liquid` (or `pages/auth/<slug>.liquid`) |
| Navigation | Theme-root `defaults.json` → `menu.items[]` |
| Live vs draft visibility | `pages.json` entry `status` (`published` vs `draft`/omitted) |

**Canonical writers on the storefront side:** `PageJsonService` + `ThemeFileService` in `flowpos-backend` (saving a page liquid can upsert `pages.json`).  

**AI Builder** proposes theme files into a **chat draft overlay**, then the merchant **ApplyDraft** writes through `themefs` → FlowPOS theme-file API. Registration for new pages is preferably `page_registry_entry` → `PageMeta` on write (FlowPOS upserts registry), not a model full rewrite of `pages.json`.

**Critical finding:** The AI Builder layer has **partial** lifecycle understanding:

- Strong: register-existing (deterministic), compound create+registry, menu merge, pages.json merge/consistency checks, draft/apply split, themecheck `page-route`.
- Weak / missing as first-class domain ops: **regenerate vs update vs recreate**, **unregister**, **duplicate page**, **remove from navigation** (no dedicated BuilderPlan op), **persisted active page target** (process-local only, added recently).
- Historical user failures (“blog not opening” → generic clarify; “create pair of pages” → validation without registry) are explained by **classifier/plan gaps and tool contracts**, not by FlowPOS lacking a page model.

This audit does **not** invent missing contracts; where the code has no distinct operation, it is marked **MISSING DOMAIN CONTRACT** or **UNDEFINED / AMBIGUOUS**.

---

## 2. Canonical Page Architecture

### 2.1 Authoritative sources

| Artifact | Path / package | Type / entrypoint | Role | Authority |
|----------|----------------|-------------------|------|-----------|
| Theme engine contract | `ai-chat/backend/internal/ai/prompts/theme_engine_spec.md` | Spec §2–§6 | Path layout, `pages.json` fields, `defaults.json` menu, boilerplate | **Authoritative convention** for generation |
| Page registry service | `flowpos-backend/.../PageJsonService.php` | `PageJsonService` | Load/assert/upsert/remove/rename rows in `pages.json` | **Authoritative** for registry on disk |
| Theme file service | `flowpos-backend/.../ThemeFileService.php` | `save` / `delete` / `move` | Write liquid; sync `pages.json` when path is a page file | **Authoritative** for file+registry coupling on save |
| Page DTO | `flowpos-backend/.../DTOs/PageDto.php` (referenced by PageJsonService) | DTO | Typed page row | Derived from registry |
| AI mirror of registry row | `ai-chat/.../themefs/pages.go` | `PageEntry` | Model/tool shape for `page_registry_entry` | Derived mirror of spec §5 |
| PageMeta on write | `ai-chat/.../themefs` + FlowPOS WriteFile | `PageMeta` | Fields FlowPOS uses to upsert registry when writing liquid | Bridge AI → FlowPOS |
| Nav schema | `ai-chat/.../themebuild/menu_merge.go` | `menuItemSchema`, `AddToMenuOperation` | `defaults.json` `menu.items[]` | **Authoritative** for AI menu merges |
| Theme FS client | `ai-chat/.../themefs` | `ThemeStore` | HTTP to FlowPOS theme-file API | I/O boundary |
| Validation | `ai-chat/.../themecheck/rule_page_route.go` | `checkPageRoute` | New liquid create must match `page_registry_entry` | Gate before stage |
| Consistency | `ai-chat/.../themebuild/page_registry_consistency.go` | `validatePageFileRegistryConsistency` | Create ↔ registry invariants | Gate before stage |
| Draft/apply | `ai-chat/.../themebuild/service.go`, `apply.go`, `writeplan.go` | `doGenerate`, `ApplyDraft`, `buildWritePlan` | Proposal → draft → apply | Lifecycle orchestration |

### 2.2 File and route conventions (from spec + PageJsonService)

- **File:** `pages/<kebab-slug>.liquid` (auth: `pages/auth/<slug>.liquid`).
- **Basename = `page` field** (and for custom pages, `slug` must equal `page`).
- **Registry:** one object per route in `pages.json` (title, slug, path, type, page, SEO*, status, …).
- **URL:** looked up via `pages.json` (root `/` → home); product/category routes are special-cased before registry.
- **Publish:** `status: "published"` required for live storefront; omitted status → draft → **404 in prod** (spec §5). This is a common “page not opening” cause.

### 2.3 What is NOT a page source of truth

- No separate MySQL “pages” table for theme routes (ThemeFileService comment: *“no DB page rows”*).
- Chat messages / BuilderPlan are **not** the registry.
- `activeTargetCache` in themebuild is **not** persisted and is **not** FlowPOS truth.

---

## 3. Page File Lifecycle

### CREATE PAGE (AI path)

```
User prompt
  → ClassifyIntent (themebuild/intent.go) + optional BuilderPlan (builderplan)
  → doGenerate (themebuild/service.go)
  → [optional] PlanCompoundWorkflow / runCompoundMultiPageCreate (compound_*.go)
     OR single-shot complex_page generateValidProposal
  → model propose_changes: action "create" on pages/<slug>.liquid
     + page_registry_entry (preferred) OR pages.json update (multi/batch)
  → incompleteAtomicPageCreateProposal / incompleteMultiPageCreateProposal / ensureCreateHasRegistry
  → themecheck Check (page-route, boilerplate, …)
  → checkAndRepair (bounded)
  → validatePageFileRegistryConsistency
  → buildWritePlan (+ attach PageMeta from registry entry)
  → persist chat_generated_files (draft)
  → merchant ApplyDraft → themefs WriteFile(+PageMeta) → ThemeFileService.save → PageJsonService.upsert
```

**Exact create meaning (implementation):**

| Question | Answer from code/spec |
|----------|------------------------|
| Which file? | `pages/<slug>.liquid` (or auth subdir) |
| Path pattern? | Spec §2; `pageRegistryWantPath` / FlowPOS `expectedPageRelativePath` |
| Who determines slug? | Model (or compound step prompt); FlowPOS upsert uses filename when meta incomplete |
| Filename? | Must match registry `page` / basename |
| Route? | Registry `slug` + `path` (`/pages` or `/pages/auth`) |
| Slug derived from filename? | **Yes for custom pages** — themecheck requires slug == page == basename |
| Metadata? | `pages.json` row (+ optional SEO fields) |
| Registration required? | **Yes** for a routable page — create liquid alone is incomplete |
| `pages.json` always required? | Theme must have it; new page must add/merge a row |
| Nav automatic? | **No** — separate `add_to_navigation` / compound menu step |
| Deterministic auto-register on create? | Only via FlowPOS when WriteFile carries PageMeta / ThemeFileService.save; AI must supply entry or pages.json merge |

### UPDATE EXISTING PAGE

```
Prompt (content rewrite / simple edit / SEO)
  → intent: complex_page / simple_edit / BuilderPlan update_page_content | update_seo_meta | simple_style_edit
  → DeepSeek (usually) propose update/edit on existing paths
  → validate / repair / stage / apply
```

Registry usually **unchanged** unless SEO fields updated via `pages.json` / `page_registry_entry` upsert.

### REGISTER EXISTING PAGE

```
Prompt matches register_existing (builderoperations.MatchesRegisterExistingPrompt)
  → BuilderPlan OpRegisterExistingPage (or Resolve from prompt)
  → builderoperations.RegisterExistingPage.Execute
  → verify liquid on disk, merge pages.json, stage liquid update + pages.json
  → ApplyDraft → FlowPOS
```

DeepSeek = 0 on this path when handled.

### ADD TO NAVIGATION

```
AddToMenuOperation / compound menu step / local menu merge
  → read store defaults.json
  → merge menu.items[] (id, label, url, children, optional pageId)
  → stage defaults.json only (structured merge — not full model rewrite)
```

Independent of registry; can reference a page that is registered or not (invariant not fully closed — see §10).

### DELETE

```
Bulk/orphan delete prompts → buildDeterministicBulkDelete
  OR model proposes pages.json drop + action "delete" on liquids
```

Spec: deleting routes requires registry edit **and** file deletes when files should disappear.

### UNREGISTER / DUPLICATE / REMOVE FROM NAV

**No dedicated BuilderPlan operation kinds** found for unregister-only, duplicate-page, or remove-from-navigation. Behavior, if any, is ad-hoc via model `pages.json`/`defaults.json` edits or delete helpers — **MISSING DOMAIN CONTRACT** at the plan layer.

---

## 4. Page Registry Lifecycle

**Authoritative:** `PageJsonService` (`flowpos-backend`).

**AI-side merge (non-authoritative until Apply):**

- `pages_registry_merge.go` — structured merge of `PageEntry` into current JSON
- `page_registry_entry` on `ai.Result` → `attachPageRegistryEntry` → `PageMeta` on write plan
- Compound: accumulate per-step registries → `buildWritePlan(extraEntries...)`

**Register existing (`RegisterExistingPage`):**

- Resolves slug from prompt/plan
- Confirms `pages/<slug>.liquid` exists
- Detects already registered → `OutcomeAlreadyDone`
- Else merges one published custom (or type-for-slug) entry
- Stages: liquid `update` (content unchanged) + `pages.json` update  
- Does **not** modify `defaults.json` / navigation

---

## 5. Navigation / Menu Lifecycle

**Authoritative file:** `defaults.json`  
**Canonical identity:** `menu` (`menu_merge.go` `canonicalMenuIdentity`)  
**Item fields:** `id`, `label`, `url`, `children`, optional `pageId`

| Question | From code |
|----------|-----------|
| URL generation | Built in merge helpers from page identity / label (store-backed merge) |
| `pageId` | Optional field on item schema |
| Ordering | Append by default (`Position: "append"`) |
| Duplicates | Menu merge validates / rejects duplicate items (see genfail `duplicate menu item`) |
| Independent of registry? | **Yes** as separate file/op — registration ≠ nav |

---

## 6. Create vs Update vs Regenerate vs Register

| Phrase | First-class op in BuilderPlan? | Actual routing today |
|--------|-------------------------------|----------------------|
| Create new page | `create_page` + `register_page` | Compound or complex_page + model |
| Update / rewrite content | `update_page_content` / full_page_edit | DeepSeek on existing liquid |
| SEO-only | `update_seo_meta` | DeepSeek / pages.json |
| Register existing | `register_existing_page` | Deterministic local op |
| Add to nav | `add_to_navigation` | Deterministic menu merge (compound) or model |
| **Regenerate page** | **No distinct op** | `isPageContentRewritePrompt` / “regenerate” cues → **complex_page content rewrite** (same family as update) — **MISSING DOMAIN CONTRACT** separating regenerate vs update vs recreate |
| Recreate / replace identity | **No** | Would look like create new slug or overwrite same file — **UNDEFINED** |
| Troubleshoot / not opening | `page_troubleshoot` + `diagnose_existing_page` (recent) | Deterministic diagnose ± registry autofix |

**Regenerate vs update:** Code comments treat “regenerate … for software company theme” as **content rewrite** (`intent.go` `isPageContentRewritePrompt` / `namedPageThemeRegenCue`), not a separate lifecycle that deletes/re-registers. Slug/identity **should** remain unless the model invents a new file — there is no enforced “preserve identity” op for regenerate.

---

## 7. Existing Page Target Resolution

| Mechanism | Location | Scoped | Persisted? | Behavior |
|-----------|----------|--------|------------|----------|
| Prompt named page (`blog`, `pricing`, …) | builderplan `troubleshootTarget`, builderoperations `namedPagePatterns` | Per request | No | Deterministic slug guess |
| `activeTargetCache` | `themebuild/active_target.go` on `Service` | tenant+chat key | **No** (memory, max 512) | Set after staged page results / diagnose; used for “now it is not working” |
| Chat history | Loaded in `doGenerate` for intent/context | Chat | Yes (messages) | **Not** a structured active-target store; model may see history when DeepSeek runs |
| BuilderPlan Targets | Plan field | Request | No | Hints only |

**Before troubleshoot work:** “blog page is not working” fell through to **ambiguous** → clarification short-circuit — **no** target resolution.  
**After:** classify → `page_troubleshoot`; active target can fill empty slug. Still **not** durable across process restarts; not FlowPOS state.

---

## 8. Current AI Builder Operation Map

| Concept | Defined by | Validated by | Executed by | Deterministic? | Can model invent paths/registry? |
|---------|------------|--------------|-------------|----------------|----------------------------------|
| ClassifyIntent | themebuild/intent.go | tests | doGenerate routing | Heuristic regex | N/A |
| BuilderPlan | builderplan/* | Validate() | observeBuilderPlan (observe + soft escalate) | CPU classifier | LM refine cannot change op kinds (ML-6) |
| SemanticRefinement | builderintelligence + ApplySemanticRefinement | Reject smuggled ops | Constraints only | Assistive | Constrained |
| ContextPlan | buildercontext / plan RequiredFiles | caps | ThemeContext packing | Mostly det. | Limited |
| builderoperations | register + diagnose | Outcome enums | Run() before DeepSeek | **Yes** | No free-form rewrite |
| compound workflow | compound_*.go | incompleteAtomic* | Per-step Generate + merge | Hybrid | Per-step liquid+entry |
| page_registry_entry | ai.Result / tools | themecheck page-route, consistency | PageMeta attach | Model supplies; platform merges | One entry; multi needs pages.json or compound |
| add_to_menu | menu_merge / compound | JSON validity, dupes | Structured merge | **Yes** when local | Model rewrite of defaults rejected/stripped |
| propose_changes | ai generator tools | validateProposal | Stage draft | Model | Yes within path allowlist |
| themecheck / repair | themecheck + checkAndRepair | findings | repair Generate | Bounded model repair | Can re-break contracts |
| staging/apply | writeplan, apply.go | consistency before stage | ApplyDraft → ThemeStore | Platform | — |

---

## 9. Plan vs Execution Mismatch Matrix

| Planned (BuilderPlan / intent) | Observed / risked actual | Files | Risk |
|--------------------------------|--------------------------|-------|------|
| `create_page` (e.g. “make the blog more professional” historically) | `update_page_content` on `blog.liquid` | blog.liquid | High — wrong op label (mitigated by existingPageContentRe) |
| `create_page` × N without compound (“pair of pages”) | Single-shot multi liquid + one `page_registry_entry` | multiple liquids | High — themecheck page-route / VALIDATION_FAILED |
| `add_to_navigation` | Model `defaults.json` full rewrite | defaults.json | High — truncation/invalid JSON (mitigated by merge) |
| `register_page` (new) | `register_existing_page` local | pages.json + liquid | Medium — naming overlap |
| Ambiguous clarify | Ran generation (historical) | any | High — fixed by clarification short-circuit |
| Troubleshoot (user) | Ambiguous clarify (historical) | none | High — “not working” ≠ improve site |
| `update_page_content` | Also SEO/pages.json when model over-edits | pages.json | Medium |
| Diagnose plan | Fix via register autofix | pages.json | Low if intentional; plan/execution should label `fix_existing_page` |
| Compound create | Index rewrite of blog.liquid | blog.liquid | High — stripped/rejected by compound guards |

Silent continue after mismatch is partially guarded by `CheckPlanExecutionMatch` / metrics; not all paths emit compare logs.

---

## 10. File / Registry / Menu Invariants

| Invariant | Enforced? | Where |
|-----------|-----------|-------|
| New `pages/*.liquid` create ⇒ matching registry | **Yes** (check) | themecheck `page-route`; `ensureProposedCreatesRegistered`; consistency validator |
| Registry row ⇒ file exists (theme integrity) | **Yes** (FlowPOS load) | `PageJsonService::loadAndAssertIntact` |
| AI draft create without registry cannot stage | **Yes** | consistency + themecheck |
| Multi-create with single `page_registry_entry` | **Rejected** | `incompleteMultiPageCreateProposal` / ensureProposedCreatesRegistered (multi) |
| Menu item ⇒ registered page | **Not fully enforced** in AI path | Menu merge does not require pages.json membership |
| Published status for live open | Spec + storefront; AI may omit → draft 404 | Spec §5; not always hard-gated in AI |
| pages.json valid JSON after merge | **Yes** | merge + serialize + validate |

---

## 11. Staging / Apply Lifecycle

```
propose (ai.Result)
  → generateValidProposal gates
  → checkAndRepair / themecheck
  → validatePageFileRegistryConsistency
  → buildWritePlan (PageMeta attach; no FlowPOS write yet)
  → EventTypeStaged + persistFileRecords (chat_generated_files)
  → ApplyStatusPending on assistant message
  → ApplyDraft: commitWritePlan / ThemeStore.WriteFile(+PageMeta)
  → FlowPOS ThemeFileService.save → optional PageJsonService.upsert
  → Preview: dashboard env=dev (drafts visible)
  → Publish: status published on registry row (live env=prod)
```

- **Atomicity:** Draft is chat-scoped overlay; apply is explicit. Compound uses checkpoints for pages.json. Partial compound failure can keep completed steps (`errCompoundPartial`).
- **Rollback:** DiscardDraft drops pending; compound registry checkpoints reduce corrupt registry risk.
- **Registry effective:** On Apply when PageMeta/pages.json write hits FlowPOS — not at propose time.

---

## 12. User Prompt → Operation Mapping (code-based)

| Prompt | Mapping from current code | Confidence |
|--------|---------------------------|------------|
| create a new blog page | `page_create` / create+register (+ DeepSeek or compound) | Defined |
| update the existing blog page | `update_page_content` / complex_page | Defined |
| rewrite blog content | same family as update | Defined |
| regenerate the blog page | **complex_page content rewrite** (cue), **not** a distinct regenerate op | **UNDEFINED / AMBIGUOUS DOMAIN CONTRACT** at plan vocabulary |
| register the blog page | `register_existing_page` local, DeepSeek=0 | Defined |
| add blog to navigation | `add_to_navigation` / menu merge | Defined |
| remove blog from navigation | No dedicated op — model defaults.json edit or **UNDEFINED** | Ambiguous |
| delete blog page | bulk delete helper and/or pages.json + delete actions | Partially defined |
| blog page is not working / not opening | `page_troubleshoot` → `diagnose_existing_page` (± autofix register) | Defined **after** troubleshoot work; historically ambiguous |
| make the site better | `ambiguous` → clarify | Defined |

---

## 13. Deterministic vs Model-Assisted Boundaries

| Operation | Classification | Why |
|-----------|----------------|-----|
| Register existing page | **DETERMINISTIC** | builderoperations |
| Diagnose existing page / registry autofix | **DETERMINISTIC** | builderoperations diagnose |
| Menu append merge | **DETERMINISTIC** | menu_merge / compound menu step |
| pages.json structured merge | **DETERMINISTIC** | pages_registry_merge |
| Compound step orchestration | **MODEL-ASSISTED** | platform steps; model writes each liquid |
| Create page copy/layout | **MODEL-REQUIRED** | content generation |
| Update/rewrite content | **MODEL-REQUIRED** | DeepSeek |
| SEO copy | **MODEL-REQUIRED** (fields may be constrained) | |
| Ambiguous clarify | **DETERMINISTIC** short-circuit | no generation |
| “Regenerate” | **CURRENTLY AMBIGUOUS** | no first-class op; routed as rewrite |
| Unregister / remove nav / duplicate | **CURRENTLY AMBIGUOUS** | no first-class ops |
| Troubleshoot non-registry bugs (broken liquid logic) | **MODEL-ASSISTED** after diagnose | diagnose local; complex fix DeepSeek (when wired) |

**ML must not learn:** pages.json merge rules, menu merge, register-existing, diagnose registry existence, clarification for empty vague prompts, slug==basename custom-page rule — these are contracts.

**ML may assist:** intent nuance, content quality, which section to edit, SEO wording, semantic constraints (audience, protect slug) **without** changing op kinds.

---

## 14. Current Failure Analysis

### Failure A — “blog page is not open / not working” → generic clarification

**Exact cause (pre-troubleshoot classify):**

1. Prompt has theme cues (`blog`, `page`) but no create/SEO/simple-style match.
2. Deterministic classifier returned **`IntentAmbiguous`** (`unclear_action`).
3. `shouldShortCircuitClarification` → merchant reply: *“What would you like to improve…”*
4. No diagnose, no registry check, no DeepSeek.

**Combined causes:** missing troubleshooting intent (historically) + clarification short-circuit treating “broken page” like “make site better”.

**Not primarily:** DeepSeek inventing registration (generation never ran).

### Failure B — “create pair of service pages” → validation after retries

**Exact cause:**

1. `"pair"` was not counted as `2` in `requestedNewPageCount` → **compound workflow not entered**.
2. Single-shot complex_page created multiple liquids.
3. `page_registry_entry` registers **one** page → themecheck `page-route` failed on unmatched creates.
4. Repair loop ×3 → `VALIDATION_FAILED` / “couldn't be validated after multiple attempts”.

**Combined causes:** count/word gap + single-entry registry contract + tool/validation (not “wrong pages.json format” as user-facing primary — underlying was **missing registration per file**).

### Failure C — “regenerate” / “improve content and design” confusion

No distinct regenerate contract; both map toward **content/complex_page**. User mental model of recreate vs rewrite is **not** encoded as separate BuilderPlan ops → **MISSING DOMAIN CONTRACT**.

---

## 15. Missing / Undefined Contracts

1. **Regenerate vs update vs recreate** — no separate operation kinds or acceptance criteria.
2. **Unregister page** — no BuilderPlan/`builderoperations` name.
3. **Remove from navigation** — no first-class op.
4. **Duplicate page** — no op.
5. **Persisted active page target** — only in-memory cache; no DB/chat column.
6. **Menu item must reference registered page** — not enforced end-to-end in AI path.
7. **Mandatory published status** on create — spec-critical for “not opening”; not always a hard AI gate.
8. **Plan/execution compare** — not universally logged on all local/diagnose paths.

---

## 16. Training / ML Requirements

**Should train (when data exists):**

- Semantic constraints (audience, protect slug/title)
- Ambiguous vs actionable intent **beyond** regex (careful not to override deterministic diagnose/register)
- Content quality for update/rewrite
- Follow-up reference language → target slug (complement active target)

**Must remain deterministic (do not train as soft ML):**

- register_existing_page
- diagnose registry/file existence autofix
- pages.json / defaults.json merges
- compound step gates
- page-route / consistency validators
- clarification for empty vague “make site better”

**Training readiness note:** Collection via `BUILDER_TRAINING_DATA_ENABLED` captures executions; troubleshooting category should be labeled when collector enabled — **do not fabricate**. Gate was NOT_READY in prior phase with near-zero tenant examples.

---

## 17. Recommended Contract To Implement Later (PROPOSAL ONLY — NO CODE)

1. Freeze vocabulary: `create_page`, `update_page_content`, `regenerate_page_content` (same identity), `recreate_page` (new identity — explicit), `register_existing_page`, `unregister_page`, `add_to_navigation`, `remove_from_navigation`, `diagnose_existing_page`, `fix_existing_page`.
2. Persist `active_target` on chat (tenant-scoped, bounded JSON).
3. Publish-status gate: creates default `published` unless merchant asked draft.
4. Enforce menu→registry soft check.
5. Always enter compound when N≥2 including word numbers (`pair`/`two`).
6. Plan/execution hard-fail on create vs update mismatch.

---

## 18. Exact Files / Functions That Would Need Changes Later

| Area | Files / symbols |
|------|-----------------|
| Op vocabulary | `builderplan/plan.go`, `planner.go`, `classify.go`, `context.go`, `refine.go` |
| Local ops | `builderoperations/registry.go`, new unregister/remove-nav ops |
| Orchestration | `themebuild/service.go` `doGenerate`, `page_troubleshoot.go`, `active_target.go` |
| Compound | `compound_workflow.go`, `compound_run.go`, `intent.go` `requestedNewPageCount` |
| Registry/menu | `pages_registry_merge.go`, `menu_merge.go`, `page_registry_consistency.go` |
| Validation | `themecheck/rule_page_route.go`, `proposal.go` gates |
| FlowPOS | `PageJsonService.php`, `ThemeFileService.php` (only if identity/publish rules change) |
| Spec/prompts | `theme_engine_spec.md` (document regenerate contract) |
| Apply/preview | `apply.go`, preview handlers |
| Training | `builderexamples`, collector fields for troubleshoot |

---

## Appendix A — Create page: who owns what

| Concern | Owner |
|---------|--------|
| Liquid body | Model (DeepSeek) or forbidden without generation |
| Basename/slug equality | Spec + themecheck |
| Registry row | `page_registry_entry` / pages.json merge / FlowPOS upsert on save |
| Nav | Separate menu merge |
| Live visibility | `status: published` in registry |

## Appendix B — “Page not opening” checklist (architecture)

From code/spec, structural causes include: missing file, missing/invalid `pages.json` row, slug/page mismatch, **draft status on prod**, missing layout markers, broken renders (soft empty), menu ≠ routing. AI diagnose op covers a subset (file, registry, markers, deps, nav mention); not full storefront HTTP 404 tracing.

---

*End of audit. No production code was modified to produce these findings beyond writing this document.*

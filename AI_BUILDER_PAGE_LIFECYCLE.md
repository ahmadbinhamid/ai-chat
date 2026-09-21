# AI Builder Page Lifecycle Contract

**contract_version = `1`**  
**Status:** FROZEN DOMAIN CONTRACT  
**Authoritative machine source:** `ai-chat/backend/internal/buildercontract/`  
**Human companion:** this document (documents the machine contract; does not override it)  
**Runtime theme reference:** `ai-chat/backend/internal/ai/prompts/theme_engine_spec.md`  
**FlowPOS registry authority:** `PageJsonService` / `ThemeFileService`  
**Prior audit:** `AI_BUILDER_PAGE_LIFECYCLE_AUDIT.md`

This phase freezes the domain model. It does **not** train ML, change DeepSeek, timeouts, retries, or repair, or implement missing operations.

---

## 1. Runtime architecture

FlowPOS theme pages are **file + JSON registry**, not database page rows.

| Concern | Authoritative store | Authority |
|---------|---------------------|-----------|
| Route / SEO / publish status | Theme-root `pages.json` | FlowPOS `PageJsonService` |
| Page body | `pages/<slug>.liquid` or `pages/auth/<slug>.liquid` | Theme files via `ThemeFileService` |
| Navigation | `defaults.json` → `menu.items[]` | Theme config; AI merges via `menu_merge` |
| AI proposals before merchant apply | Chat draft overlay (`chat_generated_files`) | `themebuild` draft / `ApplyDraft` |
| Live vs draft visibility | `pages.json` `status` | Storefront: `prod` hides drafts (404); `dev` preview shows drafts |

### Theme root layout

```
<theme-root>/
├── pages.json
├── defaults.json
├── pages/                 # pages/<slug>.liquid
│   ├── auth/              # pages/auth/<slug>.liquid
│   └── css/
├── liquid/                # layout-start, layout-end, partials/
├── components/            # + css/, js/ (js legacy)
├── css/
├── js/
└── images/
```

### Page file conventions

| Kind | Path pattern |
|------|----------------|
| Custom / content | `pages/<kebab-slug>.liquid` |
| Auth / account | `pages/auth/<slug>.liquid` |

### Custom-page identity (verified)

- `page` == liquid **basename** (no extension)
- For `type: "custom"`, **`slug` == `page`**
- Registry `path` is `/pages` or `/pages/auth`
- URL for custom pages is `/<slug>` (1:1 with slug)

`pages.json` is the authoritative route/page registry.

---

## 2. Render variables

Verified from `theme_engine_spec.md` §7. Models must **not** invent fields outside this set.

| Variable | Meaning | Source | Key fields | Typical pages |
|----------|---------|--------|------------|---------------|
| `environment` | `prod` \| `dev` | engine | — | all |
| `request` | current request | engine | `.path`, `.query` | all |
| `csrf_token` | CSRF; empty in dashboard preview | engine | string | all |
| `store` | store identity | tenant | `.id`, `.name`, `.tenant_id`, `.is_guest_checkout` | all |
| `theme` | theme metadata | theme | `.id`, `.store_id`, `.slug`, `.asset_base` | all |
| `page` | current registry row | `pages.json` | `.title`, `.slug`, `.page`, SEO/OG | all registered |
| `products` | product list | catalogue | `.items[]`, `.pagination` | home / products / lists |
| `product` | product detail | catalogue | variants, images, addons, … | product |
| `categories` | category list | catalogue | `.items[]`, `.pagination` | categories |
| `category` | category detail | catalogue | `.name`, `.slug`, `.description`, `.url` | category |
| `filters` | listing filter echo | query | search, sort, category, prices | listings |
| `filter_categories` | filter pills (≤50) | catalogue | `[{slug,name}]` | listings |
| `filter_price_range` | price bounds | catalogue | `.min`, `.max` | listings |
| `basket` | cart; **nil until created** — guard `{% if basket %}` | basket | items, totals | all (header) |
| `customer` | logged-in customer; nil logged out | auth | id, name, email, phone | all / account |
| `auth_check` | authenticated (bool-ish); layout param `customer_authenticated` | auth | true/false/1/0 | all |
| `settings` | decoded `defaults.json` | defaults | colors, font, layout, header, menu, footer | all |
| `menu` | nav from defaults | `defaults.json` → menu | `.items[]` → id, label, url, children | all |
| `path` | legacy path alias | request | prefer `request.path` | layout/header |

### Liquid capabilities (generation vocabulary)

- **Tags:** `render`, `if/elsif/else`, `for`, `assign`, `capture`, `comment`
- **Custom (layout only):** `content_for_header`, `content_for_body`, `content_for_footer`
- **Filters (preferred):** `default`, `append`, `asset_url`, `plus`, `size`, `slice`, `strip`, `upcase`, `money`, `get_products`, `escape`, `strip_html`, `truncate`
- **Forbidden:** `{% schema %}`, `{% section %}`, `{% include %}`
- **Dialect:** `nil == blank` is **FALSE** — coerce with `| default` before `| append`
- **Partials:** `{% render 'liquid/...' %}` / `{% render 'components/...' %}` with **isolated** scope
- **Assets:** `{{ 'images/x.ext' | asset_url }}` — never hardcoded `/theme-assets/...`
- **Page boilerplate:** every `pages/*.liquid` must wrap with `liquid/layout-start` + `liquid/layout-end` (spec §3)

Machine mirror: `buildercontract.RenderVariables()` / `LiquidCapabilities()`.

---

## 3. Page registry schema

One flat object per route in `pages.json`. Allowed fields (do not invent):

| Field | Role |
|-------|------|
| `title` | Display title |
| `slug` | URL slug |
| `path` | `/pages` or `/pages/auth` |
| `type` | `custom` or system type (`home`, `products`, …) |
| `page` | Basename / identity key |
| `seo_title` | `<title>` / meta |
| `seo_description` | meta description |
| `seo_keywords` | meta keywords |
| `og_title` | Open Graph title |
| `og_description` | Open Graph description |
| `og_image_path` | e.g. `images/preview.png` |
| `status` | `published` \| `draft` (omitted → **draft**) |
| `published_at` | ISO timestamp |
| `requires_auth` | account-gated routes |

**Preferred single-page upsert:** `page_registry_entry` → platform merge / `PageMeta` on write.  
**Direct `pages.json` edit:** only for multi-entry changes or removals; must preserve unrelated routes.

AI mirror type: `themefs.PageEntry`.

---

## 4. Navigation schema

**File:** `defaults.json`  
**Object:** `menu` (canonical identity `"menu"`)  
**Items:** `menu.items[]`

| Field | Required | Notes |
|-------|----------|-------|
| `id` | yes | item id |
| `label` | yes | display label |
| `url` | yes | e.g. `/blog` |
| `children` | yes (array) | one level deep |
| `pageId` | optional | optional link to page identity |

AI mutation: structured `AddToMenuOperation` merge only — **never** full-file model rewrite of `defaults.json`.

---

## 5. Operation vocabulary

Canonical set (`buildercontract.PageOperation`):

| Operation | Status |
|-----------|--------|
| `create_page` | **implemented** |
| `register_page` | **implemented** (companion of create) |
| `update_page_content` | **implemented** |
| `update_seo_meta` | **implemented** |
| `simple_style_edit` | **implemented** |
| `section_edit` | **implemented** |
| `full_page_edit` | **implemented** |
| `regenerate_page_content` | **CONTRACT_DEFINED_NOT_IMPLEMENTED** |
| `recreate_page` | **CONTRACT_DEFINED_NOT_IMPLEMENTED** |
| `register_existing_page` | **implemented** (deterministic) |
| `unregister_page` | **CONTRACT_DEFINED_NOT_IMPLEMENTED** |
| `add_to_navigation` | **implemented** (deterministic merge) |
| `remove_from_navigation` | **CONTRACT_DEFINED_NOT_IMPLEMENTED** |
| `duplicate_page` | **CONTRACT_DEFINED_NOT_IMPLEMENTED** |
| `diagnose_existing_page` | **implemented** (deterministic) |
| `fix_existing_page` | **partial** (diagnose + safe registry autofix; broader fix TBD) |
| `clarify` | **implemented** |

`BuilderPlan` may only emit **implemented/partial** ops (`DefaultAllowedOpKinds`).  
`Validate` rejects unknown kinds and `CONTRACT_DEFINED_NOT_IMPLEMENTED` kinds.  
Do **not** silently map not-implemented ops to another operation.

---

## 6. Operation matrix

| Operation | Existing Page? | New Identity? | Page File | pages.json | defaults.json | DeepSeek | Deterministic |
|-----------|----------------|---------------|-----------|------------|---------------|----------|---------------|
| `create_page` | no | yes | create | entry required | no (unless asked) | required | no |
| `register_page` | no* | yes | — | upsert | no | optional | merge yes |
| `update_page_content` | yes | no | update | usually no | no | required | no |
| `update_seo_meta` | yes | no | no | SEO fields | no | optional | merge preferred |
| `simple_style_edit` | yes | no | update | no | no | required | no |
| `regenerate_page_content` | yes | no | rewrite | preserve identity | no | required | **NOT IMPL** |
| `recreate_page` | yes | yes | replace | transition | explicit | required | **NOT IMPL** |
| `register_existing_page` | yes (file) | no | touch/no content change | merge one | **no** | **0** | **yes** |
| `unregister_page` | yes | no | **preserve** | remove row | explicit | 0 | **NOT IMPL** |
| `add_to_navigation` | resolve | no | no | no | menu merge | **0** | **yes** |
| `remove_from_navigation` | resolve | no | no | no | remove item | 0 | **NOT IMPL** |
| `duplicate_page` | yes | yes | new file | new entry | explicit | optional | **NOT IMPL** |
| `diagnose_existing_page` | yes | no | read | read | read | **0** | **yes** |
| `fix_existing_page` | yes | no | as needed | as needed | as needed | optional after diagnose | hybrid |
| `clarify` | — | — | no | no | no | n/a | yes |

\* `register_page` accompanies a new create in the same plan/workflow.

---

## 7. Operation preconditions & postconditions

### `create_page`

**Means:** Create a **brand-new** page identity.

**Preconditions:**
- Target identity does not already exist (unique slug/page)
- Theme has valid `pages.json`

**Required result:**
- `pages/<slug>.liquid` (or auth path)
- Matching `pages.json` registry entry
- Valid page-route / consistency checks

**Postconditions:**
- File ↔ registry identity match (`page`/`slug`/basename)
- Navigation **not** automatic unless `add_to_navigation` also requested
- Publish: prefer `status: "published"` when merchant expects live visibility; omitted status = draft (prod 404) — see §14

### `update_page_content`

**Means:** Modify an **existing** page while **preserving** identity.

**Preconditions:** Target page already exists.

**Postconditions:**
- Same slug, page key, filename, route
- No duplicate registry entry
- Must **not** become `create_page`
- Only requested content/style/schema changes

Examples that MUST stay here (not create): “rewrite the blog”, “make the blog more professional”, “improve the existing pricing page”.

### `update_seo_meta`

**Allowed fields only:** `seo_title`, `seo_description`, `seo_keywords`, `og_title`, `og_description`, `og_image_path`.

**Rules:** Preserve unrequested SEO fields; preserve slug/page unless explicitly supported later; structured registry update; never rewrite whole `pages.json`.

### `regenerate_page_content` — CONTRACT_DEFINED_NOT_IMPLEMENTED

**Means:** Rebuild/rewrite content of an **EXISTING** page; **same** identity (slug, page, route, registry).

**Must not:** create a second page, delete/re-register identity, change slug, duplicate navigation.

Until implemented, classifiers may still route “regenerate …” toward content rewrite family — that is a **known gap**, not a substitute contract. Do not treat it as `recreate_page`.

### `recreate_page` — CONTRACT_DEFINED_NOT_IMPLEMENTED

**Means:** Intentionally replace an existing identity with a **NEW** identity. High risk. Requires explicit merchant intent, explicit old/new slug handling, registry transition, navigation impact, and old file handling.

### `register_existing_page`

**Means:** Discover existing file → derive identity → verify not registered → merge **one** registry entry → validate → stage.

**Rules:** Does not generate content; does not create liquid; does not modify navigation; idempotent; DeepSeek = 0 when deterministic path runs.

### `unregister_page` — CONTRACT_DEFINED_NOT_IMPLEMENTED

Remove registry registration; **preserve** file. Distinct from delete.

### `add_to_navigation`

**Source:** `defaults.json` → `menu.items[]`.

**Rules:** Resolve page identity; use canonical URL; preserve existing items; reject/no-op duplicates; structured merge only; never ask model for full `defaults.json`.

### `remove_from_navigation` — CONTRACT_DEFINED_NOT_IMPLEMENTED

Remove only the requested menu item; no pages.json or liquid deletion.

### `duplicate_page` — CONTRACT_DEFINED_NOT_IMPLEMENTED

New identity based on an existing page (unique slug/page + new registry entry; nav explicit).

### `diagnose_existing_page`

Deterministic-first. Checks: file exists, registered, identity, slug/path validity, template structural validity, deps, navigation relationship when relevant.

**Structured output (contract):**

```
PageExists, Registered, IdentityValid, RouteValid, Status,
TemplateValid, NavigationState, Issues[]
```

Current Go `Diagnosis` covers most fields; `Status` as a first-class diagnose field is a small alignment gap (document only).

### `fix_existing_page`

Diagnose first → deterministic fix if possible (e.g. missing registration) → else focused model repair. No unrelated files.

---

## 8. File / registry / navigation mutation rules

| Rule | Detail |
|------|--------|
| Create liquid | Must pair with registry entry before stage |
| Multi-create | N files ⇒ N registry entries (compound); forbid single `page_registry_entry` for many files |
| Update content | Prefer `edit` over full `update`; identity preserved |
| Register existing | `pages.json` merge (+ optional liquid update touch); no `defaults.json` |
| Add nav | `defaults.json` only |
| Delete routes | Registry drop **and** liquid `delete` actions when files should disappear (theme_engine_spec §0) — not the same as unregister |
| Forbidden deletes | Never delete `pages.json`, `defaults.json`, `pages/home.liquid`, or `liquid/layout-*.liquid` as cleanup |

---

## 9. Target resolution

### Active target (`active_target`)

```json
{
  "type": "page",
  "page": "<page key>",
  "slug": "<slug>",
  "path": "pages/<slug>.liquid",
  "last_operation": "<PageOperation>",
  "contract_version": "1"
}
```

| Property | v1 contract |
|----------|-------------|
| Tenant scoped | yes (`tenantID:chatID`) |
| Chat scoped | yes |
| Bounded | yes (max 512) |
| Safe to evict | yes |
| Persisted | **no** (process memory only) |

**Persistence across restarts** = separate implementation task (not this phase).

### Resolution order

1. Explicit page name in prompt  
2. BuilderPlan operation target  
3. BuilderPlan `targets`  
4. `active_target` for tenant+chat  
5. Clarification **only** if still unresolvable  

---

## 10. Troubleshooting

Canonical phrases (“page is not working”, “not opening”, “broken”, “check and fix”, “it stopped working”) map to:

`page_troubleshoot` → `diagnose_existing_page` (± `fix_existing_page`)

**Not** generic `ambiguous` clarification when a target can be resolved.

---

## 11. Compound behavior

Enter compound when **N ≥ 2** page creations are requested.

Word quantities (must count): `two`, `pair`, `couple`, `both`, `three`, … (see `buildercontract.DefaultCompoundContract`).

Expected: per page → per registry entry → per validation/checkpoint.  
**Never:** multiple liquid files + single registry entry.

---

## 12. Publish behavior

| Status | Preview (`environment=dev`) | Live (`environment=prod`) |
|--------|-----------------------------|---------------------------|
| `published` | visible | visible |
| `draft` or omitted | visible | **404** |

**Contract:** A live/shopper-visible create must not silently leave draft when the merchant clearly asked for a published/live page.

**Gap:** AI create path does not always hard-gate `status: published` today (`RegistryRules` id `published_preferred_on_create`, `Enforced: false`). Marked as contract gap for a later implementation phase.

---

## 13. Validation invariants

| Invariant | Enforced? |
|-----------|-----------|
| New page file ⇒ matching registry | yes |
| Registry row ⇒ file exists (FlowPOS integrity) | yes |
| Multi-create + one entry | rejected |
| Unique slugs | yes (FlowPOS) |
| Menu item ⇒ registered page | **soft / not fully enforced** |
| Plan op == execution class | partial today; contract requires fail-safe on mismatch |
| Compound for N≥2 incl. word counts | yes (intent/compound) |

**Menu ↔ registry:** FlowPOS/AI allow menu URLs that are not registered pages. Soft contract: when targeting a theme page, identity **SHOULD** resolve to a registered page. Hard enforcement = future task (do not invent a restriction that contradicts current code).

---

## 14. Staging / apply lifecycle

```
propose → draft (chat overlay) → themecheck / consistency → write plan (+ PageMeta)
  → EventTypeStaged / ApplyStatusPending
  → merchant ApplyDraft → themefs WriteFile → ThemeFileService.save → PageJsonService.upsert
  → preview (dev) / live (published + prod)
```

- Registry changes become effective on **Apply**, not at propose time.
- Page file + PageMeta upsert are coupled on FlowPOS save when meta is attached.
- Compound uses checkpoints; partial failure can keep completed steps.
- DiscardDraft drops pending overlay.

---

## 15. Plan → execution hard invariant

`BuilderPlan` operation **MUST** equal actual execution class.

Examples:

| Plan | Must execute as |
|------|-----------------|
| `create_page` | create_page |
| `update_page_content` | update_page_content |
| `regenerate_page_content` | regenerate only (**not implemented** — must not fake) |
| `register_existing_page` | registration only |
| `add_to_navigation` | defaults.json menu merge only |
| `diagnose_existing_page` | diagnosis only |
| `fix_existing_page` | explicit fix |

On mismatch: record diagnostic; **fail safely** when correctness would be compromised; do not silently continue.

Machine: `buildercontract.PlanExecutionInvariants()`.

---

## 16. ML boundaries

Local ML **MUST NOT** learn or override:

- `pages.json` / `defaults.json` structure  
- page path conventions / page↔slug identity  
- registration / menu merge rules  
- compound gates / publish rules / registry validation  
- deterministic diagnostics  
- canonical `PageOperation` kinds  

Local ML **MAY** assist with: semantic constraints, audience/tone, content preferences, protected-field language, follow-up phrasing, nuanced intent when deterministic rules cannot safely decide.

**ML may NOT change canonical operation kind.**

---

## 17. DeepSeek boundaries

DeepSeek receives **focused contract slices**, never the full markdown on every request.

Keys (`buildercontract.DeepSeekContextKey`):

| Key | Use |
|-----|-----|
| `create` | new page + registry |
| `update` | preserve-identity content edits |
| `regenerate` | same-identity rebuild (contract; executor TBD) |
| `register` | existing-file registration |
| `navigation` | menu merge |
| `diagnose` | structural diagnosis |
| `fix` | diagnose-then-fix |
| `seo` | SEO/OG field updates |

---

## 18. Training metadata

Every future builder example **should** include:

| Key | Value |
|-----|-------|
| `contract_version` | `"1"` |
| `operation` | `PageOperation` |
| `plan_classification` | BuilderPlan intent/ops |
| `semantic_refinement` | refinement payload / none |
| `execution_result` | outcome |

**Do not train in this phase.**

---

## 19. BuilderPlan mappings (implemented)

| BuilderPlan `OperationKind` | Contract op | Notes |
|-----------------------------|-------------|-------|
| `create_page` | `create_page` | + usually `register_page` |
| `register_page` | `register_page` | new-page companion |
| `register_existing_page` | `register_existing_page` | local op, DeepSeek=0 |
| `update_page_content` | `update_page_content` | |
| `full_page_edit` | `full_page_edit` | |
| `section_edit` | `section_edit` | |
| `simple_style_edit` | `simple_style_edit` | |
| `update_seo_meta` | `update_seo_meta` | |
| `add_to_navigation` | `add_to_navigation` | |
| `diagnose_existing_page` | `diagnose_existing_page` | |
| `fix_existing_page` | `fix_existing_page` | partial |
| `clarify` | `clarify` | |

Not in `DefaultAllowedOpKinds` until implemented:  
`regenerate_page_content`, `recreate_page`, `unregister_page`, `remove_from_navigation`, `duplicate_page`.

---

## 20. Consistency model

**Preference A (adopted):** machine contract (`internal/buildercontract`) is **canonical**; this markdown **documents** it.

Consistency checks:

- `go test ./internal/buildercontract/ …`
- `go test ./internal/builderplan/ -run 'Contract|Validate|Plan' …`
- BuilderPlan allowed ops ⊆ contract implemented/partial ops
- Not-implemented vocabulary marked and rejected by `Validate`

---

## 21. Failure / partial behavior (summary)

| Situation | Contract behavior |
|-----------|-------------------|
| Incomplete create (liquid without registry) | Must not stage |
| Compound partial | May keep completed steps; registry checkpoints reduce corrupt registry risk |
| Register already done | Idempotent success / already-done |
| Ambiguous target on troubleshoot | Clarification; no invent |
| Plan/execution mismatch | Diagnostic + fail-safe |
| Not-implemented op in plan | Reject at Validate |

---

## 22. Later implementation candidates (not this phase)

1. First-class executors: `regenerate_page_content`, `recreate_page`, `unregister_page`, `remove_from_navigation`, `duplicate_page`  
2. Persist `active_target` (bounded JSON on chat)  
3. Hard publish-status gate on create  
4. Optional hard menu→registry check  
5. Universal plan/execution compare logging / hard-fail  

---

*End of contract_version=1. No training. No DeepSeek/timeout/retry/repair changes in this phase.*

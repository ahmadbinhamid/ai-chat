# flowPOS Storefront Theme Engine — Spec for Code Generation

This spec describes the **engine convention** every flowPOS storefront theme follows. Generate code that fits this structure exactly.

## 0. How to work (read this first)

Every extra tool call is another round trip the merchant waits through. Finish in as few turns as you can.

**Already in your context — never call a tool to fetch these:** `pages.json`, `defaults.json`, the theme's file tree, and the component library in §8. All four are supplied above this spec on every request.

**Batch your reads.** `read_theme_file` accepts up to **10 paths in one call**. Work out everything you are likely to need, then read it all at once. Do not read one file, think, then read another — that turns one round trip into five. Two batched calls should cover almost any request.

**Never read or write `liquid/layout-start.liquid` or `liquid/layout-end.liquid`.** They are spliced for you. To register a new stylesheet, return its path in `layout_links_to_add`; for a script, `layout_scripts_to_add`. §3 below is the current, complete page boilerplate, so there is never a reason to open the layout to check it.

**Where to look, by request type** — read the whole row in one batched call:

| Request | Read |
|---|---|
| Change or redesign the homepage | `pages/home.liquid`, plus only the components in it you intend to change |
| New static content page | `pages/offers.liquid` (the reference pattern) + `pages/css/page-shared.css` |
| Restyle an existing component | `components/<name>.liquid` + `components/css/<name>.css` |
| Change one page's look | `pages/<slug>.liquid` + `pages/css/<slug>.css` |
| New component | The closest existing component's `.liquid` + `.css`, as a shape reference |
| Colors, fonts, menu, footer, product columns | Nothing. `defaults.json` is already above |
| Find where something lives | One `grep_theme`, then batch-read the hits |

**Composing beats writing.** A page assembled from `{% render %}` calls against §8 is a fraction of the output of hand-written markup, streams back far faster, and inherits styling that already works. Write new markup only when nothing in §8 fits.

**Emit only what changed.** Never re-emit a file whose content is unchanged. Never emit a file you have not read. For an existing file, prefer `action: "edit"` (`old_string`/`new_string` pairs) over resubmitting the whole file as `action: "update"` — `old_string` must match the file's real current content exactly once, whitespace included; use `update` only when the change is broad enough that a full rewrite is genuinely smaller.

## 1. Template language

Liquid (Shopify-style). The engine is the full [keepsuit/liquid](https://github.com/keepsuit/php-liquid) standard library (every standard Shopify-style tag and filter — `unless`, `case`/`when`, `cycle`, `date`, `where`, `map`, `sort`, `truncate`, etc. all technically work), but keep generated code to the vocabulary below — it's what every existing file already uses, and a component staying inside it is easier for the next turn to read and edit.

- Tags: `{% render '<path>', key: value, ... %}`, `{% if %}/{% elsif %}/{% else %}/{% endif %}`, `{% for x in y %}/{% endfor %}` (with `forloop.first`/`forloop.last`), `{% assign x = y %}`, `{% capture x %}...{% endcapture %}`, `{% comment %}...{% endcomment %}` (or whitespace-trimmed `{%- comment -%}`).
- Filters: `| default: x`, `| asset_url`, `| plus: 0`, `| size`, `| slice: a, b`, `| strip`, `| upcase`, `| money` (formats a number as GBP, e.g. `12.5 | money` → `£12.50` — every price field already arrives pre-formatted as `*_formatted`, so reach for this only when computing a NEW amount, e.g. a discounted price), `| get_products` (fetches specific products by slug for a hand-picked row — input must be an array: `"slug-a,slug-b" | split: ',' | get_products` — returns the list shape from §7, silently drops slugs that don't resolve), `| escape` / `| strip_html` / `| truncate: n`.
- No `{% schema %}`, no `{% section %}`, no theme-editor JSON blocks. `render` is the only include mechanism — **there is no `{% include %}` tag**; using it is a parse error, not a silent no-op.
- Render path is always prefixed by its root folder: `'liquid/...'`, `'components/...'` — never a bare name.
- Pass only explicit params to `render` — never rely on implicit/global scope leaking into a component.
- Booleans from the backend are inconsistent (`true`/`false`/`1`/`0`/`"1"`/`"0"`) — always guard with `{% if x == true or x == 1 %}`, never a bare truthy check, when reading a boolean-ish field.

**Escaping.** `{{ value }}` writes raw bytes — nothing is auto-escaped. Two things make this safe in practice: visitor-supplied values (`filters.*`, `customer.*`, `request.query`) already arrive pre-escaped, and `escape` here is **idempotent** (unlike stock Shopify — escaping an already-escaped string is a no-op, so it's always safe to add defensively). `product.description`/`category.description` are the opposite — deliberately **raw, unescaped** staff-authored HTML — render them raw where you want that markup (`<div class="description">{{ product.description }}</div>`), and pipe through `strip_html`/`escape` where you don't (a `<meta name="description">` tag, a `<title>`). Always escape a value written into an HTML *attribute* (`alt="{{ product.name | escape }}"`). Never interpolate a string into an inline `<script>` body — use a field that's already pre-encoded JSON (`product.variants_json`) or a `data-*` attribute instead.

## 2. Directory layout

```
<theme-root>/
├── defaults.json          theme config: colors, fonts, layout tokens, header/footer/menu defaults (see §6)
├── pages.json             route registry + SEO metadata, one entry per page (see §5)
├── css/                   global CSS, not scoped to one page/component (base.css, auth.css)
├── js/                    global scripts, loaded on every page via layout-end.liquid, in a fixed order (see §8)
├── images/                static assets, referenced via `{{ 'images/x.ext' | asset_url }}`
├── liquid/
│   ├── layout-start.liquid   opens <html>/<head>, all <link rel=stylesheet>, opens <body>, renders header, opens <main>
│   ├── layout-end.liquid     closes </main>, renders footer + minicart, all <script> tags, closes </body></html>
│   └── partials/             small shared includes, called with explicit params (account-sidebar, account-loader, product-list-item)
├── components/                self-contained sections ("blocks"): header, footer, hero, product grids, testimonials, forms...
│   ├── css/<name>.css          — one CSS file per component.liquid, same basename
│   └── js/<name>.js            — legacy/unused; do not add new logic here (see §8)
└── pages/                    one Liquid file per route, kebab-case filename = URL slug
    ├── auth/                  account/auth routes (login, register, my-orders, ...)
    └── css/<name>.css         — one CSS file per page.liquid, same basename, plus page-shared.css (shared hero/breadcrumb chrome for simple content pages)
```

No `layouts/`, `sections/`, `templates/`, or `locales/` folders exist. Single layout, single (English) locale.

The two `liquid/layout-*.liquid` files are listed here so you understand the structure, not so you edit them. See §0.

## 3. Mandatory page boilerplate

**Every** file in `pages/` (including `pages/auth/`) must open and close with exactly this — never deviate, never add/remove a param:

```liquid
{% render 'liquid/layout-start',
  page: page,
  store: store,
  menu: menu,
  path: path,
  theme: theme,
  customer: customer,
  customer_authenticated: auth_check,
  environment: environment,
  csrf_token: csrf_token
%}

<!-- page body here -->

{% render 'liquid/layout-end', theme: theme, store: store %}
```

Everything the page renders goes between those two calls, normally inside one wrapping `<section>`.

This block is always current. Copy it from here rather than reading an existing page to find it.

## 4. Composing a page from components

Prefer composing existing components over writing new markup. Example — `pages/home.liquid` in full:

```liquid
{% render 'liquid/layout-start', page: page, store: store, menu: menu, path: path, theme: theme, customer: customer, customer_authenticated: auth_check, environment: environment, csrf_token: csrf_token %}
<section class="sf-page sf-home">
  {% render 'components/store-hero-banner', theme: theme %}
  {% render 'components/feature-cards-row', theme: theme %}
  {% render 'components/tips-teaser', theme: theme %}
  {% render 'components/product-grid-block', products: products.items, title: 'Best Sellers', theme: theme %}
  {% render 'components/subscribe-section', theme: theme %}
  {% render 'components/product-grid-block', products: products.items, title: 'New Arrivals', theme: theme %}
  {% render 'components/contact-inquiry', theme: theme %}
  {% render 'components/testimonials', theme: theme %}
  {% render 'components/card-essentials', theme: theme %}
</section>
{% render 'liquid/layout-end', theme: theme, store: store %}
```

A simple custom content page (`pages/offers.liquid`, in full — this is the pattern for any new "static content" page: hero + prose section):

```liquid
{% render 'liquid/layout-start', page: page, store: store, menu: menu, path: path, theme: theme, customer: customer, customer_authenticated: auth_check, environment: environment, csrf_token: csrf_token %}
<section class="page-hero">
  <div class="page-hero-inner">
    <div class="breadcrumb"><a href="/">Home</a> / Offers</div>
    <h1>Offers &amp; Deals</h1>
    <p>Current promotions, bundles and seasonal savings.</p>
  </div>
</section>
<section class="page-section">
  <div class="page-section-inner content-prose" style="text-align:center;">
    <p>Browse current deals in our <a href="/products">shop</a>.</p>
    <a href="/products" class="btn-primary">Shop Now</a>
  </div>
</section>
{% render 'liquid/layout-end', theme: theme, store: store %}
```

`page-hero` / `page-hero-inner` / `breadcrumb` / `page-section` / `page-section-inner` / `content-prose` / `btn-primary` are already styled in `pages/css/page-shared.css` — reuse them for any new plain content page instead of inventing new classes.

Both examples above are complete files. For a simple content page you can usually write it straight from this section without reading anything.

## 5. Routing — `pages.json`

One flat object per route. Adding a page = adding an entry here **and** creating the matching `pages/<slug>.liquid` file (or `pages/auth/<slug>.liquid` if it needs the account-page treatment):

```json
{
  "title": "Offers",
  "slug": "offers",
  "path": "/pages",
  "type": "custom",
  "page": "offers",
  "seo_title": "Offers & Deals | Store Name",
  "seo_description": "...",
  "seo_keywords": "...",
  "og_title": "...",
  "og_description": "...",
  "og_image_path": "images/preview.png",
  "status": "published",
  "published_at": "2026-07-20T00:00:00+00:00",
  "requires_auth": false
}
```

Rules:
- `type: "custom"` for any new content/landing page. `page` **must** equal the `.liquid` file's basename (no extension) in `pages/`. An unrecognized `type` value silently falls back to `custom` — there is no `"cart"` type (the basket page is `type: "basket"`; `/cart` is just whatever slug a theme happens to name it).
- System route types (`home`, `products`, `product`, `categories`, `category`, `basket`, `login`, `register`, `forget_password`, `reset_password`, `verify_otp`, `my_account`, `my_orders`, `change_password`) are fixed, one-per-type, and already exist — never create a second entry of these types.
- `path: "/pages/auth"` for anything under `pages/auth/`; `path: "/pages"` otherwise.
- `requires_auth: true` only for account-gated routes (`my_account`, `my_orders`, `change_password`) — an anonymous visitor is redirected to `/login?redirect=…`. The guest-only auth routes (`login`, `register`, `forget_password`, `verify_otp`, `reset_password`) work the other way: a customer who's already logged in is redirected to `/account`. Both redirects are automatic — never build this logic in the page itself.
- `/product/{slug}` and `/category/{slug}` are matched before `pages.json` at all — they always resolve to the `product`/`category` template with that slug's data, regardless of what's registered. Every other path is looked up against `pages.json` verbatim (root `/` normalizes to `home`).
- `page.seo_title`, `page.title`, `page.seo_description`, `page.seo_keywords` from this record are what the layout puts in `<title>`/`<meta>` — always fill them in with real copy for a new page, never a placeholder or an ellipsis.
- The current `pages.json` is already in your context. Check it there for slug collisions; do not read the file.

## 6. `defaults.json` — theme config

Top-level keys: `snippets_path` (always `"liquid"`), `colors` (named brand colors, ~45 keys), `font` (`family`, `serif`, `size`), `layout` (`radius`, `sectionSpacing`, `shadow`), `header` (search/account/cart toggles, logo, sticky, announcement bar), `showAnnouncement` (`enabled`, `messages[]`, `speed`), `menu.items[]` (`id`, `label`, `url`, `children[]`, `pageId`), `footer` (columns, newsletter, copyright, social links, logo), `productList.columns` (`desktop`/`tablet`/`mobile` counts).

**Do not read `theme.colors.*` etc. directly in Liquid** — nothing in the theme does. Colors/fonts/layout reach CSS as platform-injected `--theme-*` / `--layout-*` CSS custom properties (generated from this file above the theme's own stylesheets), and every component stylesheet consumes them with a literal fallback: `var(--theme-primary, #1e3a8a)`. When writing new CSS, follow the same pattern — never hardcode a color that should come from `defaults.json`; use `var(--theme-<key>, <sane-fallback>)`.

Only add/change a top-level key in `defaults.json` if a component you're generating actually needs a new configurable value (e.g. a new menu item, a new social link). Don't restructure existing keys.

The current `defaults.json` is already in your context — read it there, not with a tool.

## 7. Data model (context variables)

Only reference fields listed here. If a page needs data not listed, say so instead of inventing a field name.

| Object | Fields |
|---|---|
| `page` | `.title`, `.seo_title`, `.seo_description`, `.seo_keywords` |
| `store` | `.name` |
| `theme` | forwarded to every render call; rarely dereferenced directly (`theme.asset_base` is the one exception, used as an image fallback base path) |
| `menu` | `.items[]` → `.label`, `.url`, `.active` (0/1/bool), `.children[]` (same shape, one level deep) |
| `path` | current request path string |
| `customer` | `.name`, `.email`, `.phone` |
| `customer_authenticated` (page param name: pass as `auth_check`) | bool-ish |
| `environment` | string, interpolated into `<body class="env-{{ environment }}">` |
| `csrf_token` | string, meta tag only |
| `product` (product detail page) | Every list-shape field below **plus**: `.title` (alias of `.name`), `.can_quick_add`/`.show_add_to_cart` (1/0, same value — addable without an option picker), `.images[]` (`.url`), `.variant_count`, `.price_formatted`, `.price_amount`, `.compare_at_price_formatted`, `.on_sale`, `.has_choices`, `.choices[]` (`.id`, `.label`, `.items[]` → `.id`, `.name`), `.has_variants`, `.variants[]` (`.id`, `.label`, `.price_amount`, `.price_formatted`, `.image_url`, `.images[]`, `.sku`, `.barcode`, `.is_available`, `.options` — map of choice_type_id → choice_item_id, for driving a `<select>`), `.default_variant_id`, `.variants_json` (the `.variants` array pre-encoded as JSON — use this, never hand-build JSON from the fields above, when handing variant data to a script), `.url`, `.has_addons`, `.addon_groups[]`/`.add_on_groups[]` (`.id`, `.name`, `.min_selection`, `.max_selection`, `.is_required`, `.addons[]` → `.id`, `.name`, `.price_amount`, `.price_formatted`, `.max_quantity`, `.is_active`), `.addons[]` (every add-on flattened, each carrying `.group_id`/`.group_name`) |
| `products` (list contexts) | `.items[]` → `.id`, `.name`, `.title` (alias), `.slug`, `.url`, `.image_url`, `.price_amount`, `.price_formatted`, `.compare_at_price_formatted`, `.on_sale`, `.sku`, `.barcode`, `.description` (**raw, unescaped** — staff HTML), `.default_variant_id`, `.has_variants`, `.can_quick_add`/`.show_add_to_cart`; `.pagination` → `.page`, `.last_page`, `.total`, `.per_page` (always 15), `.has_prev`, `.has_next`, `.prev_page`, `.next_page` |
| `category` | `.name`, `.slug`, `.description` (raw, unescaped), `.url`, `.image_url` |
| `categories` | `.items[]` (category shape), `.pagination` (same shape as products.pagination) |
| `filter_categories` | Up to 50 `[{slug, name}]` — filter pills |
| `filters` | `.search`, `.sort` (`name_asc`/`name_desc`/`price_asc`/`price_desc`), `.category`, `.min_price`, `.max_price`, `.per_page` (always 15) — echoes the listing page's own `?search=&sort=&category=&min_price=&max_price=` query params back, pre-escaped, so a filter form can render its own state. There is no separate JSON search endpoint — a filter/sort control is a normal `<form method="get">`/`<a href="?sort=...">` against the current page. |
| `filter_price_range` | `.min`, `.max` — bounds across the visible catalogue, for a price-range input |
| `basket` | `nil` when the shopper has no basket yet — **always guard with `{% if basket %}`**. `.items[]` → `.id`, `.variant_id`, `.product_slug`, `.name`, `.note`, `.quantity`, `.price`, `.sub_total`, `.total_discount`, `.total`, `.image_url`, `.variant` (`.id`, `.items[]` → `.id`, `.name`, `.choice_type_label`), `.extensions_data.addons[]` (`.id`, `.name`, `.price`, `.quantity`) — both `.variant` and `.extensions_data.addons` are only present when that line actually has one, so guard before looping; `.item_count`, `.sub_total`, `.total`, `.total_discount`, `.shipping_charges`, `.customer_name`/`.customer_email`/`.customer_phone` |

Client-side data (after page load) comes from `window.StorefrontApi` (`js/storefront-api.js`), which is a thin wrapper around the session-cookie-authenticated storefront API — always reuse it for a new authenticated call instead of writing a raw `fetch()` (it resolves the CSRF token and same-origin credentials for you): `GET`/`PUT /api/basket` (PUT **replaces** the whole item array — read, modify, send back, never an incremental "add one"), `POST /api/customer/{login,register,verify-registration,resend-registration-otp,forgot-password,reset-password,change-password,logout,my-account,my-orders}`, `GET /api/customer/my-orders`. Checkout is a plain link, not a fetch call: `<a href="/pre-checkout">`.

## 8. Component library

Render with `{% render 'components/<name>', ... %}`. Props beyond `theme` are optional unless marked required; omitting an optional prop falls back to a sensible default already baked into the component.

This table is the authoritative signature list. You do not need to read a component to learn its props — read it only when you are going to change it.

| Component | Signature | Notes |
|---|---|---|
| `header` | `menu, path, theme, store, customer, customer_authenticated` | Full `<header>`: announcement bar, logo, search (`GET /products`), account/cart icons, `{% render 'components/header-menu' %}` for the nav row, mobile menu. One per theme — do not re-render inside a page. |
| `header-menu` | `menu, path` | Just the `<ul>` nav, looping `menu.items` (+ one level of `.children`). |
| `footer` | `theme, store` | Full `<footer>`: CTA strip, column grid, social, copyright. One per theme. |
| `minicart` | *(none)* | Cart drawer skeleton; populated client-side by `js/minicart.js`. Render once per page, right before `layout-end`. |
| `product-grid-block` | `products` (array, **required**), `title` or `props.title`, `theme`; optional `props.showImage/showName/showPrice/showAddToCart` (bool), `props.moreUrl`, `props.moreLabel`, `props.moreAriaLabel`, `root_attrs`, `preload_scripts` | Server-rendered horizontal carousel of products with qty stepper + add-to-cart. Use for "Best Sellers" / "New Arrivals" / any curated product row. |
| `product-grid` | *(none)*, JS-hydrated | "Featured collections" tile grid skeleton — data comes from JS, not Liquid. |
| `store-hero-banner` | `theme` | Homepage hero slider — currently one hardcoded slide. To add real slides, extend this component's markup (loop) rather than hardcoding a second copy. |
| `feature-cards-row` | `theme` | 3-up trust/feature icon row (e.g. "Free Delivery"). Hardcoded content — edit in place for new copy/icons. |
| `tips-teaser` | `theme` | Image + text "About the brand" teaser block. Hardcoded content. |
| `subscribe-section` | `theme` | Newsletter/coupon promo banner. Hardcoded content. |
| `contact-inquiry` | `theme` | Contact form + photo, 2-column. Submit is intercepted client-side by `js/contact-inquiry.js` (builds a `mailto:` link) — no server POST. |
| `testimonials` | `theme`; optional `variant` (`'about'` \| `'light'` \| default), `title`, `show_title` (bool), `show_image` (bool), `background` (CSS color string) | Only explicit render params are read — never ambient page state — so a page can never leak into this component's output. Reuse this contract for any new parameterized component you write. |
| `card-essentials` | `theme` | "Latest Blogs" 3-card teaser grid. Hardcoded content — edit in place. |
| `store-related-products` | `theme` | Related-products carousel skeleton, JS-populated on product pages. |

`liquid/partials/` (small includes, always called with explicit params, no `theme` default-forwarding needed unless used):

| Partial | Signature | Notes |
|---|---|---|
| `product-list-item` | `product` (required), `theme` | The product card used by `/products` and `/category` grids. `product.has_variants == true/1` → "View details" link; else, if `product.default_variant_id` present → "Add to Cart" button with `data-pl-add-to-cart`; else → "View details" fallback. |
| `account-sidebar` | `active` (`'profile'` \| `'orders'` \| `'password'`), `customer` | Left nav for logged-in account pages. |
| `account-loader` | `type` (`'profile'` \| `'orders'` \| `'password'`) | Skeleton/shimmer loading state. |

**Do not** render `components/js/*.js` behavior — those files are legacy/unwired duplicates. Real component logic lives in top-level `js/`.

## 9. CSS conventions

- Plain CSS, no framework (no Tailwind/Bootstrap). One file per component/page, same basename, in the matching `css/` sibling folder.
- **Every page loads every CSS file** (no per-page conditional loading). A new `pages/css/<name>.css` or `components/css/<name>.css` will not be picked up unless it is registered — but you do **not** edit `liquid/layout-start.liquid` to register it. Return the new file's theme-relative path in `layout_links_to_add` and the `<link rel="stylesheet" href="{{ '<path>' | asset_url }}">` tag is spliced into the layout for you.
- Class naming: prefer the theme's `t1-<component-abbrev>-*` convention for new component/page markup (e.g. `t1-pd-*` product detail, `t1-pl-*` product list, `t1-pgb-*` product-grid-block, `t1-fc-*` feature cards, `t1-rp-*` related products). For simple static content pages, reuse the existing generic `page-hero`, `page-section`, `content-prose`, `btn-primary`, `breadcrumb` classes from `pages/css/page-shared.css` instead of inventing new ones.
- Design tokens: consume shared values via `var(--theme-<key>, <fallback>)` / `var(--layout-<key>, <fallback>)` (see §6) — always supply the fallback. Component-local palette/sizing gets its own `--<component>-*` custom properties scoped to that stylesheet. When a component needs a color with no `--theme-*` equivalent (a translucent overlay, a glass effect, a one-off accent), declare it once as a `--<component>-*` custom property at the top of that stylesheet and reference it via `var(--<component>-*)` everywhere else — never the raw value inline on a `color`/`background`/`background-color`/`border-color`/`fill`/`stroke` declaration. The custom-property form is only a warning; the raw inline form is a hard error.
- Base tokens already defined in `css/base.css` (`:root`): `--sf-bg`, `--sf-surface`, `--sf-text`, `--sf-muted`, `--sf-border`, `--sf-accent`, `--sf-accent-dark`, `--sf-radius`, `--sf-container`. `sf-*` prefixed classes (`sf-page`, `sf-container`, `sf-btn`, `sf-grid`, etc.) are the generic layout/utility layer — safe to reuse on any new page.
- Keep `data-*` attributes (JS hooks) and CSS classes (styling) as separate concerns — never select on a class in JS, never rely on a `data-*` attribute for styling.
- Font: the heading font is already loaded by the layout; system sans stack for body text. Do not add font `<link>` tags.

## 10. JS conventions

- Vanilla JS only. No framework, no bundler, no build step.
- All page-behavior scripts live in top-level `js/` and are `<script src="..." defer>`'d at the end of the layout, in this fixed order: `theme.js`, `header.js`, `storefront-api.js`, `minicart.js`, `product-grid-block.js`, `testimonials.js`, `contact-inquiry.js`, `products.js`, `store-faq.js`, `auth-password.js`. If a new page needs new interactive behavior, create the file in `js/` and return its path in `layout_scripts_to_add` — do **not** edit `liquid/layout-end.liquid` yourself. It is appended after the existing list, which is after `storefront-api.js`, so an API-dependent script is safe.
- Each script self-guards: query the relevant root element/class first and `return`/no-op if absent, so the same global script is safe to load on every page regardless of whether its markup is present.
- Use `data-<component-abbrev>-<purpose>` attributes as JS hooks (e.g. `data-pd-add-to-cart`, `data-minicart-count`, `data-pl-view-grid`) — write new JS against new `data-*` hooks you define in the markup, never against CSS classes.
- `window.StorefrontApi` (`js/storefront-api.js`) is the only API client — reuse it for any new authenticated call (handles CSRF resolution, fetch wrapping) instead of writing raw `fetch()` calls.
- IIFE wrapper per file: `(function () { 'use strict'; ... })();` or `(() => { ... })();` — either is fine, keep it self-contained (no globals except `window.StorefrontApi` and whatever the file itself intentionally exposes).

## 11. Naming & routing rules

- `pages/<kebab-case-slug>.liquid` → route `/<kebab-case-slug>`, 1:1, matching `pages.json`'s `slug`/`page` fields for `type: "custom"` entries.
- Auth/account pages: `pages/auth/<name>.liquid`, `path: "/pages/auth"` in `pages.json`.
- CSS/JS files mirror their Liquid file's basename exactly (`pages/foo.liquid` ↔ `pages/css/foo.css`; `components/bar.liquid` ↔ `components/css/bar.css`).
- Never use a `.php`/`.blade.php`/`.twig`/`.jsx`/`.tsx` extension anywhere in a theme — `.liquid`, `.css`, `.js`, `.json`, image extensions only.

## 12. Hard rules (must / must not)

1. **Must** open/close every `pages/*.liquid` file with the exact boilerplate in §3 — no missing/extra/reordered params.
2. **Must not** introduce `{% schema %}`, `{% section %}`, `{% include %}`, or any theme-editor JSON block — none of these exist in this Liquid dialect and an unknown *tag* (unlike an unknown filter or variable) is a hard parse error, not a silent no-op. Stick to the tags/filters in §1's vocabulary; the wider standard-library tags/filters mentioned there exist and won't error, but introduce one only when nothing in §1's list can do the job.
3. **Must not** invent data fields not listed in §7. If new data is required, state that a new backend field is needed instead of fabricating one.
4. **Must not** introduce a CSS or JS framework/library (no Tailwind, Bootstrap, React, Vue, jQuery, build tooling).
5. **Must** register any new `pages/css/*.css` or `components/css/*.css` path in `layout_links_to_add`, and any new `js/*.js` path in `layout_scripts_to_add`. **Must not** read or emit `liquid/layout-start.liquid` or `liquid/layout-end.liquid` — the splice is automatic.
6. **Must** add a matching `pages.json` entry (§5) for any new route, with real (non-placeholder) SEO fields.
7. **Prefer** composing existing components (§8) over writing new bespoke markup; only add a new component file when nothing existing fits, and give it the same three-file shape (`components/<name>.liquid` + `components/css/<name>.css`, only add JS if genuinely interactive).
8. **Must** guard boolean-ish fields with `{% if x == true or x == 1 %}` (§1), and guard absent/optional data with `{% if x != blank %}` before rendering it.
9. **Must not** hardcode a value that already has a `defaults.json`/`--theme-*` equivalent (colors, fonts, spacing) — reference the token with a fallback instead.
10. Keep output minimal and scoped to what was asked — don't refactor unrelated components, don't add extra sections the user didn't request, don't add code comments narrating what a line does (Liquid comments are fine only to record a genuine non-obvious constraint, as `product-list-item.liquid` and `testimonials.liquid` already do).
11. **Must not** write placeholder, lorem ipsum, or "TODO" text as page content, and must not leave a `pages.json` SEO field as a stand-in. If the request is too vague to write real content, set `needs_clarification: true` with an empty `files` array and ask the merchant, rather than filling a page with a marker.
12. **Must not** re-emit a file whose content is unchanged, and must not emit a file you have not read. **Prefer** `action: "edit"` over `action: "update"` for a targeted change to an existing file — a full rewrite only when genuinely simpler. Call `propose_changes` exactly once, with the complete final set of changes.
13. **Must** diagnose and fix a broken page yourself rather than surfacing a technical error to the merchant — see §13. A merchant reporting "this page is broken" or pasting an error/screenshot does not know what Liquid, a template, or a syntax error is; treat it as a bug report to investigate, not a question to relay back.

## 13. Debugging a broken page

The merchant is not a developer. When they report a blank section, a broken layout, a page that "doesn't work," or paste an error message or screenshot from the preview, that is a bug report — investigate and fix it yourself. Never ask the merchant to explain, describe, or paste the specifics of a technical error; never respond with the raw error text; never tell them "there's a syntax error in your Liquid file" and stop there. Read the file(s) yourself (`read_theme_file`/`grep_theme`) and look for the specific cause before proposing anything.

**Read the actual failure message first if one was given** — it usually names the exact file and line. Common causes, roughly in order of how often they turn out to be it:

- **A `{% render %}` target doesn't exist**, or is missing its root-folder prefix (`'components/x'`/`'liquid/x'`, never bare `'x'`). Check the referenced path is real in the current file tree before assuming the component itself is broken.
- **Unbalanced tags** — a `{% for %}`/`{% if %}`/`{% capture %}` missing its matching `{% endfor %}`/`{% endif %}`/`{% endcapture %}`, often from a hand-edited or partially-applied change. Read the whole file, not just the section that looks wrong; the actual imbalance is often earlier than the visible symptom.
- **A `>` (or `<`) inside a Liquid expression embedded in an HTML tag's own attributes** — e.g. `<div{% if a.size > 0 %} data-x{% endif %}>` — reads as fine Liquid but is easy to mis-edit into something that closes the tag early or never closes it. Check attribute-embedded conditionals character by character when a tag "goes missing" right after one.
- **A field referenced that isn't in §7**, or a typo in a field name (`.slug` vs `.slgu`) — this fails *silently* (renders blank, no error) per this engine's non-strict mode, so "a section is just empty" is often this, not a crash. Cross-check every dotted reference against §7's field list.
- **A `pages.json` entry pointing at a file that doesn't exist**, or a `path`/`page` combination that doesn't resolve to `{path}/{page}.liquid` — the route itself would 404/redirect rather than error, but a bad `type` value (e.g. an invented one like `"cart"` — see §5) silently falls back to `custom`, which changes what catalogue data loads and can make a page that expects `product`/`category` to be populated render as if it's empty.
- **An asset path is wrong** — a hardcoded `/theme-assets/...`/absolute path instead of `{{ 'path' | asset_url }}`, or a path with a leading `/` or `..` (both make `asset_url` return an empty string).
- **A raw color or a `var(--theme-*)` with no fallback** where `checkThemeToken` would have already caught this in a proposal you wrote — but a merchant can also inherit this from theme content that predates you; fix it the same way (§9) when you find it.

If you fix the underlying file, propose the correction directly — same rules as any other change (§0, §12: read before you edit, prefer `action: "edit"`, real content only). If you read the relevant files and the cause genuinely isn't one of the above, say plainly and briefly what you checked and what you found (in merchant language — "the [section name] on this page isn't loading correctly, here's what I found" — never raw error text, stack traces, or Liquid/Go internals), and either propose your best fix or ask one specific, non-technical clarifying question ("should X show Y or Z when there's no image?") rather than a request to explain a technical error.
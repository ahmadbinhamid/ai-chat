# flowPOS Storefront Theme Engine — Spec for Code Generation

Reference implementation: theme `jpronumbingcream`. This spec describes the **engine convention** every theme follows — not the branding of that one theme. Generate code that fits this structure exactly.

## 1. Template language

Liquid (Shopify-style), simplified dialect. Only these tags/filters exist — do not use anything else:

- Tags: `{% render '<path>', key: value, ... %}`, `{% if %}/{% elsif %}/{% else %}/{% endif %}`, `{% for x in y %}/{% endfor %}` (with `forloop.first`/`forloop.last`), `{% assign x = y %}`, `{% capture x %}...{% endcapture %}`, `{% comment %}...{% endcomment %}` (or whitespace-trimmed `{%- comment -%}`).
- Filters: `| default: x`, `| asset_url`, `| plus: 0`, `| size`, `| slice: a, b`, `| strip`, `| upcase`.
- No `{% schema %}`, no `{% section %}`, no theme-editor JSON blocks. `render` is the only include mechanism.
- Render path is always prefixed by its root folder: `'liquid/...'`, `'components/...'` — never a bare name.
- Pass only explicit params to `render` — never rely on implicit/global scope leaking into a component.
- Booleans from the backend are inconsistent (`true`/`false`/`1`/`0`/`"1"`/`"0"`) — always guard with `{% if x == true or x == 1 %}`, never a bare truthy check, when reading a boolean-ish field.

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
    <p>Special savings on numbing creams, gels, sprays and bundles.</p>
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
- `type: "custom"` for any new content/landing page. `page` **must** equal the `.liquid` file's basename (no extension) in `pages/`.
- System route types (`home`, `products`, `product`, `categories`, `category`, `cart`, `basket`, `login`, `register`, `forget_password`, `reset_password`, `verify_otp`, `my_account`, `my_orders`, `change_password`) are fixed, one-per-type, and already exist — never create a second entry of these types.
- `path: "/pages/auth"` for anything under `pages/auth/`; `path: "/pages"` otherwise.
- `requires_auth: true` only for account-gated routes (`my_account`, `my_orders`, `change_password`).
- `page.seo_title`, `page.title`, `page.seo_description`, `page.seo_keywords` from this record are what `layout-start.liquid` puts in `<title>`/`<meta>` — always fill them in for a new page, don't leave placeholders.

## 6. `defaults.json` — theme config

Top-level keys: `snippets_path` (always `"liquid"`), `colors` (named brand colors, ~45 keys), `font` (`family`, `serif`, `size`), `layout` (`radius`, `sectionSpacing`, `shadow`), `header` (search/account/cart toggles, logo, sticky, announcement bar), `showAnnouncement` (`enabled`, `messages[]`, `speed`), `menu.items[]` (`id`, `label`, `url`, `children[]`, `pageId`), `footer` (columns, newsletter, copyright, social links, logo), `productList.columns` (`desktop`/`tablet`/`mobile` counts).

**Do not read `theme.colors.*` etc. directly in Liquid** — nothing in the theme does. Colors/fonts/layout reach CSS as platform-injected `--theme-*` / `--layout-*` CSS custom properties (generated from this file above the theme's own stylesheets), and every component stylesheet consumes them with a literal fallback: `var(--theme-primary, #1e3a8a)`. When writing new CSS, follow the same pattern — never hardcode a color that should come from `defaults.json`; use `var(--theme-<key>, <sane-fallback>)`.

Only add/change a top-level key in `defaults.json` if a component you're generating actually needs a new configurable value (e.g. a new menu item, a new social link). Don't restructure existing keys.

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
| `product` (product detail page) | `.name`, `.id`, `.slug`, `.sku`, `.barcode`, `.description`, `.image_url`, `.images[]` (`.url`), `.price_formatted`, `.price_amount`, `.compare_at_price_formatted`, `.on_sale`, `.has_choices`, `.choices[]` (`.id`, `.label`, `.items[]` → `.id`, `.name`), `.has_variants`, `.variants[]` (`.id`, `.label`, `.price_amount`, `.price_formatted`, `.image_url`, `.sku`, `.is_available`), `.default_variant_id`, `.variants_json`, `.url` |
| `products` (list contexts) | `.items[]` (product shape above minus detail-only fields), `.pagination` → `.page`, `.last_page`, `.total`, `.per_page`, `.has_prev`, `.has_next`, `.prev_page`, `.next_page` |
| `category` | `.name`, `.slug`, `.description`, `.url`, `.image_url` |
| `categories` | `.items[]` (category shape), `.pagination` (same shape as products.pagination) |
| `filter_categories` | `[{slug, name}]` — filter pills |
| `filters` | `.search`, `.sort`, `.category`, `.min_price`, `.max_price` |
| `filter_price_range` | `.min`, `.max` |
| `basket` | `.items[]` → `.variant_id`, `.name`, `.quantity`, `.price_formatted`, `.total_formatted`; `.subtotal_formatted` |

Client-side data (after page load) comes from `window.StorefrontApi` (`js/storefront-api.js`): `POST /api/customer/{login,register,verify-registration,resend-registration-otp,forgot-password,reset-password,change-password,logout,my-account,my-orders}`, `GET /api/store/products?search=&page=`.

## 8. Component library

Render with `{% render 'components/<name>', ... %}`. Props beyond `theme` are optional unless marked required; omitting an optional prop falls back to a sensible default already baked into the component.

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
- **Every page currently `<link>`s every CSS file** in `liquid/layout-start.liquid` (no per-page conditional loading). When you add a new `pages/css/<name>.css` or `components/css/<name>.css`, **add its `<link rel="stylesheet" href="{{ '<path>' | asset_url }}">` to `liquid/layout-start.liquid`** in the same list, alongside the existing ones — it will not be picked up otherwise.
- Class naming: prefer the theme's `t1-<component-abbrev>-*` convention for new component/page markup (e.g. `t1-pd-*` product detail, `t1-pl-*` product list, `t1-pgb-*` product-grid-block, `t1-fc-*` feature cards, `t1-rp-*` related products). For simple static content pages, reuse the existing generic `page-hero`, `page-section`, `content-prose`, `btn-primary`, `breadcrumb` classes from `pages/css/page-shared.css` instead of inventing new ones.
- Design tokens: consume shared values via `var(--theme-<key>, <fallback>)` / `var(--layout-<key>, <fallback>)` (see §6) — always supply the fallback. Component-local palette/sizing gets its own `--<component>-*` custom properties scoped to that stylesheet.
- Base tokens already defined in `css/base.css` (`:root`): `--sf-bg`, `--sf-surface`, `--sf-text`, `--sf-muted`, `--sf-border`, `--sf-accent`, `--sf-accent-dark`, `--sf-radius`, `--sf-container`. `sf-*` prefixed classes (`sf-page`, `sf-container`, `sf-btn`, `sf-grid`, etc.) are the generic layout/utility layer — safe to reuse on any new page.
- Keep `data-*` attributes (JS hooks) and CSS classes (styling) as separate concerns — never select on a class in JS, never rely on a `data-*` attribute for styling.
- Font: Google Fonts `Montserrat` (loaded via `<link>` in `layout-start.liquid`) for headings; system sans stack for body text.

## 10. JS conventions

- Vanilla JS only. No framework, no bundler, no build step.
- All page-behavior scripts live in top-level `js/` and are `<script src="..." defer>`'d at the end of `liquid/layout-end.liquid`, in this fixed order: `theme.js`, `header.js`, `storefront-api.js`, `minicart.js`, `product-grid-block.js`, `testimonials.js`, `contact-inquiry.js`, `products.js`, `store-faq.js`, `auth-password.js`. If a new page needs new interactive behavior, add a new file here and append its `<script>` tag to that list (after `storefront-api.js` if it depends on the API client).
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
2. **Must not** introduce a new templating tag, filter, or `{% schema %}`/`{% section %}` block — this is not standard Shopify Liquid, only the tags/filters listed in §1 exist.
3. **Must not** invent data fields not listed in §7. If new data is required, state that a new backend field is needed instead of fabricating one.
4. **Must not** introduce a CSS or JS framework/library (no Tailwind, Bootstrap, React, Vue, jQuery, build tooling).
5. **Must** register any new `pages/css/*.css` or `components/css/*.css` file's `<link>` tag in `liquid/layout-start.liquid`, and any new `js/*.js` file's `<script>` tag in `liquid/layout-end.liquid`.
6. **Must** add a matching `pages.json` entry (§5) for any new route, with real (non-placeholder) SEO fields.
7. **Prefer** composing existing components (§8) over writing new bespoke markup; only add a new component file when nothing existing fits, and give it the same three-file shape (`components/<name>.liquid` + `components/css/<name>.css`, only add JS if genuinely interactive).
8. **Must** guard boolean-ish fields with `{% if x == true or x == 1 %}` (§1), and guard absent/optional data with `{% if x != blank %}` before rendering it.
9. **Must not** hardcode a value that already has a `defaults.json`/`--theme-*` equivalent (colors, fonts, spacing) — reference the token with a fallback instead.
10. Keep output minimal and scoped to what was asked — don't refactor unrelated components, don't add extra sections the user didn't request, don't add code comments narrating what a line does (Liquid comments are fine only to record a genuine non-obvious constraint, as `product-list-item.liquid` and `testimonials.liquid` already do).

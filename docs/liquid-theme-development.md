# Liquid Theme Development Guide

> Source: FlowPOS Liquid Theme Development PDF. Canonical runtime reference for storefront Liquid.
> AI generation rules live in `backend/internal/ai/prompts/theme_engine_spec.md` (embedded into the model). This file is the fuller human/runtime reference.


## PDF page 1

Liquid Theme Development
Guide
A reference for developers building FlowPOS storefront themes. It
documents everything the backend hands to a template at render time:
the data payload, the custom tags and ﬁlters, the standard Liquid feature
set, the theme package layout, and the HTTP endpoints a theme's
JavaScript is allowed to call.
Everything here is derived from the implementation in app/Modules/
Builder and app/StoreFront. Where behaviour deviates from
Shopify Liquid, that is called out explicitly.
Table of contents
1. How a page is rendered
2. Theme package structure
3. pages.json — the page registry
4. defaults.json — theme settings
5. URL routing and page types
6. Global variable reference
7. Custom tags
8. Custom ﬁlters
9. Standard Liquid tags and ﬁlters
10. Partials
11. Assets
12. Storefront HTTP API
13. Escaping and security
14. Resource limits and error behaviour
15. Extending the payload from PHP
16. Quick reference

## PDF page 2

1. How a page is rendered
Every storefront request follows the same path:
1. Store identiﬁcation. IdentifyOnlineStore resolves the
request host to a tenant domain, loads the tenant's OnlineStore
and its single active Theme, and builds a StoreContext. If the
domain is unknown, the store is missing, the store is in
maintenance mode, or no theme is active, the request fails before
Liquid ever runs.
2. Page resolution. PageResolver turns the URL path into a
template name and looks it up in the theme's pages.json, then
resolves that entry to a ﬁle on disk.
3. Access check. PageAccessGuard enforces publish state and
customer authentication. This step is skipped entirely in the
builder preview so staﬀ can view draft and login-only pages while
editing.
4. Data assembly. LiquidDataBuilder builds the payload
described in section 6 — catalogue data, basket, store, page
metadata, theme settings, and any module-contributed keys.
5. Render. LiquidEngine parses and renders the page template,
with the theme folder mounted as the ﬁlesystem root for partials.
The response is always text/html; charset=UTF-8 with X-
Content-Type-Options: nosniff.
Rendering environments
There are two environments, exposed to templates as {{
environment }}:
Value Where it comes from Behaviour

## PDF page 3

prod A real shopper request
on the storefront domain
Only published pages, active
products and published
categories are visible. Auth rules
are enforced.
dev The dashboard theme
preview (GET /api/
store/render/
{path?})
Draft pages, inactive products
and unpublished categories are
all visible. Auth rules are
skipped.
Always guard preview-only markup with {% if environment ==
'dev' %} rather than assuming what a shopper sees.
2. Theme package structure
A theme is a folder on the private disk at store/{storeId}/themes/
{theme-slug}. Two ﬁles at the theme root are mandatory in practice:
my-theme/
├── pages.json            # required — the page registry
├── defaults.json         # theme settings, exposed as {{ settings }}
├── pages/                # page templates (one per pages.json entry)
│   ├── home.liquid
│   ├── products.liquid
│   ├── product.liquid
│   ├── category.liquid
│   ├── basket.liquid
│   └── login.liquid
├── liquid/               # layout fragments and shared partials
│   ├── layout-start.liquid
│   ├── layout-end.liquid
│   └── partials/
│       └── product-list-item.liquid
├── components/           # reusable sections
│   ├── header.liquid
│   └── footer.liquid

## PDF page 4

├── css/
├── js/
└── images/
Folder names other than the two JSON ﬁles are a convention, not a
requirement — {% render %} resolves any path relative to the theme
root.
There is no layout ﬁle
FlowPOS Liquid has no {% layout %} tag and no theme.liquid
wrapper. Each page template is rendered standalone and is responsible
for its own complete HTML document. The established pattern is to split
the shell into two partials and render them at the top and bottom of every
page:
{% render 'liquid/layout-start' %}
  <main class="page-home">
    ...
  </main>
{% render 'liquid/layout-end' %}
Note that {% render %} creates an isolated scope — see section 10
for what that means for variables.
Allowed ﬁle extensions
The theme ﬁle editor accepts only these extensions:
liquid, png, jpg, webp, css, js, mjs, json, mp4, webm, mov, ogg, m4v
An imported theme package may additionally carry:
jpeg, gif, svg, ico, woff, woff2, ttf, otf, eot, html, htm, txt, md, map

## PDF page 5

Anything outside both lists is silently dropped during import, as are dot-
ﬁles (.env, .git, .htaccess) and any symlink pointing outside the
source folder.
3. pages.json — the page registry
pages.json is a JSON array at the theme root. It is the single source
of truth for which pages exist, where their template ﬁles live, and
whether they are visible.
[
  {
    "title": "Home",
    "slug": "home",
    "path": "/pages",
    "type": "home",
    "page": "home",
    "status": "published",
    "requires_auth": false,
    "seo_title": "Home | Welcome to Our Store",
    "seo_description": "Discover our latest products, offers, and curated collections.",
    "seo_keywords": "home, store, shop online",
    "og_title": "Home | Welcome to Our Store",
    "og_description": "Discover our latest products, offers, and curated collections.",
    "og_image_path": "images/preview.png",
    "published_at": null
  }
]
Field Required Description
title Yes Human-readable page name.
Falls back to the template
name if empty.

## PDF page 6

page Yes The template ﬁle name
without extension. This is the
key that identiﬁes the page.
slug — The URL segment. Defaults
to page when omitted.
Lookups match either ﬁeld.
path — Folder inside the theme
holding the template.
Defaults to pages.
type — One of the page types.
Defaults to custom.
status — published or draft.
Defaults to draft. A draft
page returns 404 in prod.
requires_auth — When true, an anonymous
visitor is redirected to /
login?redirect=….
Accepts true, 1, "yes",
"on".
seo_title,
seo_description,
seo_keywords
— Surfaced on {{ page }}.
og_title,
og_description,
og_image_path
— Surfaced on {{ page }}
for Open Graph tags.
published_at — ISO 8601 timestamp, set
automatically when a page is
ﬁrst published.
Rules enforced by the backend:
•  Slugs must be unique within a theme. Both slug and page
participate in the uniqueness check.
•  The ﬁle at {path}/{page}.liquid must exist. .html and an
extension-less ﬁle are accepted as fallbacks.

## PDF page 7

•  path and page are validated against directory traversal — a row
such as {"path": "../../..", "page": ".env"} will
never resolve.
•  If pages.json is missing, unparseable, or lists a ﬁle that does
not exist, the theme is rejected as corrupt at import time.
pages.json and defaults.json are never served over the public
asset route, even though they sit in the theme folder.
4. defaults.json — theme settings
defaults.json is a free-form JSON object at the theme root. Its entire
decoded contents are exposed to templates as {{ settings }}, so
the structure is yours to deﬁne. A typical shape:
{
  "colors": {
    "primary": "#00b72f",
    "background": "#ffffff",
    "footerBg": "#1a2b4b"
  },
  "font": { "family": "Inter", "size": "14px" },
  "layout": { "radius": "10px", "sectionSpacing": "40px" },
  "header": {
    "searchEnabled": true,
    "searchPlaceholder": "Search products…",
    "showCart": true,
    "cartUrl": "/cart"
  },
  "menu": {
    "items": [
      { "id": "d08c44bb", "label": "Home", "url": "/", "children": [] },
      { "id": "23433bc2", "label": "Shop All", "url": "/shop", "children": [] },
      {
        "id": "37e9f072",
        "label": "Clothing",

## PDF page 8

"url": "/category/dog-clothing",
        "type": "category",
        "children": []
      }
    ]
  }
}
The menu.items key is treated specially: it is also published separately
as {{ menu.items }}. Everything else is read through {{
settings.* }}.
Always pair a settings lookup with default: so a theme still renders
when a tenant clears a value:
--color-primary: {{ settings.colors.primary | default: '#00b72f' }};
If defaults.json is missing or contains invalid JSON, {{ settings
}} is an empty object rather than an error. The ﬁlename must be exactly
defaults.json at the theme root — default.json or settings/
defaults.json will not be read.
5. URL routing and page types
Two URL shapes carry a resource slug and are matched before
anything else:
URL Template
used
Resource loaded
/product/
{slug}
product The product with that slug, into
{{ product }}
/category/
{slug}
category The category with that slug, into
{{ category }}
Every other path is used verbatim as a template lookup key against

## PDF page 9

pages.json. The root path / normalises to home.
The type ﬁeld in pages.json determines catalogue loading and
access rules:
type Catalogue data
loaded
Access rule
home products,
categories
listings
—
products products,
categories
listings
—
product Single product
(detailed)
404 if the slug does
not resolve
categories products,
categories
listings
—
category Single
category plus
its products
404 if the slug does
not resolve
custom products,
categories
listings
—
basket None —
login, register,
forget_password,
verify_otp,
reset_password
None Guest only — a
logged-in customer is
redirected to /
account
my_account,
my_orders,
change_password
None Login required — an
anonymous visitor is
redirected to /
login?
redirect=…

## PDF page 10

An unrecognised type value falls back to custom.
404 behaviour
When a page cannot be resolved, a browser request is redirected to /
rather than shown a 404 page. Two cases still return a real 404: a failure
on the home page itself (redirecting would loop), and requests that
expect JSON.
6. Global variable reference
Every variable below is available at the top level of a page template.
Undeﬁned variables render as an empty string rather than raising an
error, so a missing key is a silent blank.
6.1 environment
String, either dev or prod. See rendering environments.
6.2 request
Property Type Description
request.path string Normalised route path, e.g. products or
product/red-mug. Never has a
leading slash; / becomes home.
request.query object The full query string as a nested object.
HTML-escaped, including nested arrays.
6.3 csrf_token
The session CSRF token. Required on every write request a theme

## PDF page 11

makes to the storefront API — see section 12. Empty string in the
dashboard preview, which has no session.
<meta name="csrf-token" content="{{ csrf_token }}">
6.4 store
Property Type Description
store.id int Online store id.
store.name string Store display name.
store.tenant_id int Owning tenant id.
store.maintenance_mode bool Always false in a rendered
page — maintenance mode
short-circuits before render.
store.is_guest_checkout bool Whether checkout is
allowed without an account.
6.5 theme
Property Type Description
theme.id int Theme row id.
theme.store_id int Owning store id.
theme.slug string Theme slug, used in asset URLs.
theme.asset_base string Public URL preﬁx for this theme's
assets, e.g. /theme-asset/
store/1/themes/petshop-7.
Absolute (including host) in dev.
Prefer the asset_url ﬁlter over building paths from
theme.asset_base by hand.

## PDF page 12

6.6 page
Page metadata for <head>. Loaded from the matching pages.json
row, with product and category pages overriding the title from the
resource itself.
Property Type Description
page.title string Resource name on product/
category pages, otherwise the
pages.json title.
page.slug string Page slug, falling back to the
template name.
page.page string Template key, falling back to the
template name.
page.seo_title string On product/category pages this
becomes `"{Resource name}
page.seo_description string From pages.json.
page.seo_keywords string From pages.json.
page.og_title string From pages.json.
page.og_description string From pages.json.
page.og_image_path string From pages.json. A theme-
relative path — pass it through
asset_url.
<title>{{ page.seo_title | default: page.title }} — {{ store.name }}</title>
{% if page.seo_description %}
  <meta name="description" content="{{ page.seo_description | escape }}">
{% endif %}
{% if page.og_image_path %}
  <meta property="og:image" content="{{ page.og_image_path | asset_url }}">
{% endif %}
6.7 products — the product listing

## PDF page 13

Present on home, products, categories, category and custom
pages. Always an object with two keys, even when empty.
{% for product in products.items %}
  <a href="{{ product.url }}">{{ product.name }} — {{ product.price_amount | money }}</a>
{% else %}
  <p>No products found.</p>
{% endfor %}
Each entry in products.items carries the list shape:
Property Type Description
id int Product id.
name string Product name.
title string Alias of name.
slug string URL slug.
url string Canonical path, /
product/{slug}.
image_url string First product image,
falling back to the
default variant's ﬁrst
image. Empty string
when there is none.
price_amount ﬂoat Lowest variant price
when the product has
option variants,
otherwise the default
variant or product price.
price_formatted string price_amount
rendered as GBP, e.g.
£12.50.

## PDF page 14

compare_at_price_formatted string Formatted compare-at
price when on sale,
otherwise an empty
string.
on_sale int 1 or 0.
sku string Product SKU.
barcode string Default variant barcode,
falling back to the
product barcode.
description string Raw, unescaped —
may contain staﬀ-
authored HTML.
default_variant_id int /
null
Id of the default variant.
has_variants int 1 when the product has
non-default option
variants.
can_quick_add int 1 when the product can
be added to the basket
without an option
picker.
show_add_to_cart int Same value as
can_quick_add.
Booleans in catalogue data are deliberately 1/0 integers rather than
true/false, so {% if product.has_variants %} behaves
predictably.
products.pagination
Property Type Description
total int Total matching products.
per_page int Always 15.

## PDF page 15

page int Current page number.
last_page int Final page number.
has_next bool Whether a next page exists.
has_prev bool Whether a previous page exists.
next_page int / null Next page number, or null.
prev_page int / null Previous page number, or null.
{% if products.pagination.has_prev %}
  <a href="?page={{ products.pagination.prev_page }}">Previous</a>
{% endif %}
<span>Page {{ products.pagination.page }} of {{ products.pagination.last_page }}</span>
{% if products.pagination.has_next %}
  <a href="?page={{ products.pagination.next_page }}">Next</a>
{% endif %}
6.8 product — the product detail page
Only populated on a product page. It contains every ﬁeld from the list
shape plus the following.
Property Type Description
images array [{ "url": "…" }]. Falls back to a
single-entry array built from image_url.
variants array Option variants — see below.
variant_count int Number of option variants.
variants_json string The variants array pre-encoded as
JSON, for handing straight to a script.
choices array Attribute groups (Size, Colour, …) as {
id, label, items: [{ id, name
}] }.
has_choices int 1 when choices is non-empty.

## PDF page 16

addon_groups array Add-on groups across the default and
option variants, deduplicated.
add_on_groups array Alias of addon_groups.
addons array All add-ons ﬂattened, each carrying
group_id and group_name.
has_addons int 1 when addon_groups is non-empty.
Each entry in product.variants:
Property Type Description
id int Variant id — this is what the
basket API expects.
label string Choice item names joined with /,
falling back to the SKU, then
Option #{id}.
sku, barcode string Variant identiﬁers.
price_amount ﬂoat Variant price.
price_formatted string Formatted variant price.
is_available int 1 or 0.
image_url string First variant image.
images array [{ "url": "…" }].
choice_item_ids array Choice item ids that deﬁne this
variant.
options object Map of choice_type_id →
choice_item_id, for driving
selects.
addon_groups /
add_on_groups
array Add-on groups for this variant.
addons array Flattened add-ons for this variant.
Each add-on group:

## PDF page 17

Property Type Description
id, name int,
string
Group identity.
min_selection,
max_selection
int Selection bounds.
is_required int 1 when min_selection > 0.
addons array { id, name, price,
price_amount,
price_formatted,
max_quantity, vat_rate,
is_taxable, is_active,
sort_order }.
Groups with no active add-ons are dropped entirely.
<script>window.PRODUCT_VARIANTS = {{ product.variants_json }};</script>
{% for group in product.choices %}
  <label>{{ group.label }}</label>
  <select name="choice_{{ group.id }}">
    {% for item in group.items %}
      <option value="{{ item.id }}">{{ item.name }}</option>
    {% endfor %}
  </select>
{% endfor %}
6.9 categories and category
categories mirrors the products shape — categories.items
plus categories.pagination. category is a single object on a
category page.
Property Type Description
id int Category id.

## PDF page 18

name string Category name.
slug string URL slug.
url string Canonical path, /category/
{slug}.
description string Raw, unescaped.
image_url string Thumbnail URL, or an empty string.
On a category page, products holds that category's products with the
same listing and pagination shape.
6.10 filters, filter_categories,
filter_price_range
filters echoes the currently applied query parameters back to the
template, HTML-escaped, so ﬁlter controls can render their own state.
Property Type Description
filters.search string Current search term.
filters.sort string One of name_asc,
name_desc, price_asc,
price_desc.
filters.category string Current category slug ﬁlter.
filters.min_price,
filters.max_price
string Normalised price bounds, or
empty strings.
filters.per_page int Always 15.
filter_categories is a non-paginated list of up to 50 categories
(same shape as a categories.items entry) for rendering ﬁlter pills.
filter_price_range is { min, max } ﬂoats across the visible
catalogue, for range inputs.
Accepted query parameters on listing pages:

## PDF page 19

Parameter Eﬀect
page Page number, minimum 1.
search Matched against product name, description and
SKU.
sort name_asc (default), name_desc, price_asc,
price_desc.
category Restrict to a category slug.
min_price,
max_price
Price bounds. Non-numeric values are ignored,
and inverted bounds are swapped.
<form method="get">
  <input type="search" name="search" value="{{ filters.search }}">
  <select name="sort">
    <option value="name_asc"  {% if filters.sort == 'name_asc' %}selected{% endif %}>Name A–Z</option>
    <option value="price_asc" {% if filters.sort == 'price_asc' %}selected{% endif %}>Price low to high</option>
  </select>
  <input type="number" name="min_price" value="{{ filters.min_price }}"
         min="{{ filter_price_range.min }}" max="{{ filter_price_range.max }}">
</form>
6.11 basket
The current session basket, or nil when the shopper has no basket yet.
Always guard with {% if basket %}.
Property Type Description
basket.items array Basket lines.
basket.item_count int Sum of all line
quantities.
basket.sub_total number Subtotal before
shipping and
discount.
basket.total number Order total.

## PDF page 20

basket.total_discount number Total discount
applied.
basket.shipping_charges number Shipping cost.
basket.customer_name,
basket.customer_email,
basket.customer_phone
string /
null
Contact details
captured on the
basket.
Each line in basket.items:
Property Type Description
id int Basket line id.
variant_id int /
null
Variant this line refers to.
product_slug string /
null
For linking back to the
product page.
name string Line display name.
note string /
null
Customer note on the line.
quantity int Quantity.
price number Unit price.
sub_total,
total_discount, total
number Line money values.
image_url string /
null
Variant image, then variant
product image, then
product image.
variant object Present when the line has
a variant: { id, items:
[{ id, name,
choice_type_label,
choice_type_id }] }.

## PDF page 21

extensions_data.addons array Present when the line has
add-ons: { id,
extension_id, name,
price, quantity }.
{% if basket and basket.item_count > 0 %}
  <ul>
    {% for line in basket.items %}
      <li>
        <img src="{{ line.image_url }}" alt="{{ line.name | escape }}">
        <a href="/product/{{ line.product_slug }}">{{ line.name }}</a>
        × {{ line.quantity }} — {{ line.total | money }}
        {% for addon in line.extensions_data.addons %}
          <small>+ {{ addon.name }} ({{ addon.quantity }})</small>
        {% endfor %}
      </li>
    {% endfor %}
  </ul>
  <p>Total: {{ basket.total | money }}</p>
{% else %}
  <p>Your basket is empty.</p>
{% endif %}
6.12 customer and auth_check
auth_check is a boolean: whether a storefront customer is logged in.
customer is nil when they are not, otherwise an object with four
HTML-escaped ﬁelds.
Property Type
customer.id int
customer.name string
customer.email string
customer.phone string

## PDF page 22

{% if auth_check %}
  <a href="/account">Hi, {{ customer.name }}</a>
{% else %}
  <a href="/login">Sign in</a>
{% endif %}
6.13 settings and menu
settings is the entire decoded defaults.json — see section 4.
menu.items is the normalised menu.items array from the same ﬁle.
Each menu item carries id, label, url and children. The backend
deliberately does not mark the active item; compare against
request.path yourself.
<nav>
  {% for item in menu.items %}
    <a href="{{ item.url }}" {% if item.url == '/' and request.path == 'home' %}class="active"{% endif %}>
      {{ item.label }}
    </a>
    {% if item.children.size > 0 %}
      <ul>
        {% for child in item.children %}
          <li><a href="{{ child.url }}">{{ child.label }}</a></li>
        {% endfor %}
      </ul>
    {% endif %}
  {% endfor %}
</nav>
6.14 Module-contributed variables
Other backend modules can add their own top-level key to the payload
(loyalty points, reviews, and so on). These are merged last but cannot
overwrite anything above — the following keys are reserved and refused
at registration time:

## PDF page 23

auth_check, basket, categories, category, csrf_token, customer, environment,
filter_categories, filter_price_range, filters, menu, page, product, products,
request, settings, store, theme
If a contributing module fails, its key is simply omitted and the rest of the
page renders. Treat these keys as optional and always guard them.
7. Custom tags
FlowPOS adds three tags on top of standard Liquid. They render the
markup of every enabled backend integration (analytics scripts, pixels,
third-party embeds) at a ﬁxed point in the document. Which integrations
exist, and in what order, is decided in PHP — a theme only decides
where they land.
Tag Placement Injection
point
{% content_for_header
%}
Immediately before
</head>
head
{% content_for_body
%}
Immediately after
<body>
body_start
{% content_for_footer
%}
Immediately before
</body>
body_end
None of them accept parameters; passing any is a syntax error.
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>{{ page.seo_title }}</title>
  {% content_for_header %}
</head>
  {% content_for_body %}

## PDF page 24

<!-- page markup -->
  {% content_for_footer %}
</body>
</html>
In the two-partial layout convention, {% content_for_header %}
and {% content_for_body %} belong at the end of layout-
start.liquid and {% content_for_footer %} at the start of
layout-end.liquid, so every page picks them up automatically.
Every theme should include all three. Each integration's output is
preﬁxed with an HTML comment marker (<!-- app:google-tag-
manager -->) so it is identiﬁable in page source. If one integration
throws, its output is dropped and the others still render.
8. Custom ﬁlters
asset_url
Turns a theme-relative path into a public asset URL.
<link rel="stylesheet" href="{{ 'css/theme.css' | asset_url }}">
<img src="{{ 'images/logo.png' | asset_url }}" alt="{{ store.name | escape }}">
Behaviour:
•  Backslashes are normalised to forward slashes and leading
slashes are stripped.
•  Returns an empty string for a blank path or any path containing
...
•  Uses theme.asset_base. Inside a {% render %} scope
where that is not visible, it rebuilds the base from theme.slug
and theme.store_id/store.id. With no theme context at all it

## PDF page 25

falls back to a root-relative /{path}.
money
Formats an amount as GBP with two decimal places.
{{ 10.07 | money }}      → £10.07
{{ 1234.5 | money }}     → £1,234.50
{{ '12.08' | money }}    → £12.08
{{ nil | money }}        → (empty string)
{{ 'abc' | money }}      → (empty string)
Amounts are major currency units (pounds, not pence), matching
every price ﬁeld in the catalogue and basket payloads. String inputs
have ,, £ and spaces stripped before parsing. Non-numeric and empty
input yields an empty string rather than £0.00.
The currency symbol is currently ﬁxed to £.
get_products
Fetches speciﬁc products by slug — for hand-picked "featured" rows that
the listing payload does not cover.
{% assign featured = "red-mug,blue-mug,green-mug" | split: "," | get_products %}
{% for product in featured %}
  {% render 'liquid/partials/product-list-item', product: product %}
{% endfor %}
Behaviour:
•  The input must be an array, so pipe a comma-separated string
through split ﬁrst. Anything else returns an empty array.
•  Results come back in the order the slugs were given. Slugs that
do not resolve are skipped silently, so the result may be shorter
than the input.
•  Returns the list shape from section 6.7, not the detailed product

## PDF page 26

shape.
•  Visibility follows the current environment: in prod, inactive and
unpublished products are excluded.
escape (overridden)
escape is replaced with an idempotent version: escaping an already-
escaped string leaves it unchanged.
This matters because visitor-supplied values (filters.*,
request.query, customer.*) are already escaped on their way into
the payload. With standard Liquid's double-encoding escape, writing {{
filters.search | escape }} — the correct, defensive thing to do
— would display &lt;script&gt; to the shopper instead of their
search term. Here it renders correctly.
This is a deliberate deviation from Shopify, where escape double-
encodes and escape_once does not. In FlowPOS the two ﬁlters
behave identically. If you were relying on double-encoding a value on
purpose, it will not work.
9. Standard Liquid tags and ﬁlters
FlowPOS runs keepsuit/liquid 0.11, a PHP port of Shopify Liquid. The
full standard library is available.
Tags
assign, break, capture, case / when, continue, cycle,
decrement, doc, echo, for / else, ifchanged, if / elsif / else,
increment, liquid, raw, render, tablerow, unless
Filters

## PDF page 27

Strings — append, prepend, capitalize, downcase, upcase,
escape, escape_once, newline_to_br, remove, remove_first,
remove_last, replace, replace_first, replace_last, slice,
split, strip, lstrip, rstrip, squish, strip_html,
strip_newlines, truncate, truncatewords, url_encode,
url_decode, base64_encode, base64_decode
Numbers — abs, at_least, at_most, ceil, floor, round, plus,
minus, times, divided_by, modulo
Arrays — compact, concat, first, last, join, map, reverse,
size, slice, sort, sort_natural, sum, uniq, where, reject,
has, find, find_index
Other — date, default
Note that PHP method names are converted to snake_case, so
atLeast is written at_least in a template.
Non-strict mode
Both strict variables and strict ﬁlters are disabled. An undeﬁned variable
renders as an empty string and an unknown ﬁlter passes its input
through unchanged, rather than raising. This keeps a small typo from
taking down a live storefront — but it also means typos fail silently, so
verify output rather than assuming a blank means "no data".
10. Partials
{% render %} is the only way to pull in a partial. There is no {%
include %} tag — it is not part of this Liquid port, and because
unknown tags are a parse error (unlike unknown variables and ﬁlters),
using it will break the page.
Partials are resolved from the theme root. Paths are relative to that root
and the .liquid extension is optional.

## PDF page 28

{% render 'components/header' %}
{% render 'liquid/partials/product-list-item', product: product %}
{% render 'components/product-grid', items: products.items, heading: 'New in' %}
Scope isolation. {% render %} gives the partial a fresh scope: it sees
only the variables passed to it explicitly, not the page's globals. This is
why asset_url has a fallback path for reconstructing the asset base. If
a partial needs product, settings or theme, pass them in:
{% render 'components/header', settings: settings, menu: menu, customer: customer %}
Path rules. Template names may contain only letters, digits,
underscores, dots, hyphens and forward slashes, and may not begin
with / or .. Anything else is a syntax error. Paths are canonicalised and
conﬁrmed to sit inside the theme root, so traversal outside the theme is
impossible.
Missing partials do not break the page. A reference to a template that
does not exist renders as an empty string and logs a warning naming
the template. The rest of the page renders normally. This is a deliberate
trade-oﬀ — a typo in one snippet costs that snippet's output rather than
white-screening every shopper — so check the application log when a
section mysteriously disappears.
11. Assets
Theme assets are served from /theme-asset/store/{storeId}/
themes/{theme-slug}/{path} — exactly what asset_url
produces.
Servable types. Only inert asset types are served: css, js, mjs, json,
map, txt, svg, png, jpg, jpeg, gif, webp, ico, woff, woff2, ttf,
otf, eot, mp4, webm, mov, ogg, m4v.
Never servable. .liquid source, .html/.htm ﬁles, pages.json,
defaults.json, and anything under a dot-directory. Requesting them

## PDF page 29

returns 404.
Caching. Assets are sent with a 300-second max-age and revalidate
against Last-Modified. Because these URLs are not ﬁngerprinted, a
theme edit reuses the same path — up to ﬁve minutes can pass before
shoppers see a changed stylesheet. Add your own cache-busting query
string if you need an immediate update:
<link rel="stylesheet" href="{{ 'css/theme.css' | asset_url }}?v={{ theme.id }}">
Cross-origin. A storefront host only serves its own store's assets. SVGs
are served with a restrictive Content-Security-Policy that blocks
any script inside them, so an SVG will render in <img> but not as an
interactive document.
12. Storefront HTTP API
The storefront runs under Laravel's web middleware group, so these
endpoints use the session cookie for identity — no bearer token is
involved.
Two rules apply to every write:
1. CSRF token required. Send {{ csrf_token }} as an X-
CSRF-TOKEN header (or a _token ﬁeld).
2. Same-origin only. A write carrying an Origin header from
another host is rejected with 403.
<meta name="csrf-token" content="{{ csrf_token }}">
const csrf = document.querySelector('meta[name="csrf-token"]').content;
async function post(url, body) {
  const response = await fetch(url, {
    method: 'POST',
    headers: {

## PDF page 30

'Content-Type': 'application/json',
      'Accept': 'application/json',
      'X-CSRF-TOKEN': csrf,
    },
    credentials: 'same-origin',
    body: JSON.stringify(body),
  });
  return response.json();
}
Basket
Method Path Body Returns
GET /api/
basket
— { "basket": … } in the
same shape as {{ basket
}}, or null.
PUT /api/
basket
{ "items":
[...] }
The updated basket.
PUT replaces the basket contents with the array you send — it is not an
incremental "add one item" call. Read the current basket, modify the
array, send it back. Each item identiﬁes a variant_id and quantity;
the basket is created on ﬁrst write if the session does not have one.
Customer authentication
Login, register, OTP and password endpoints are rate limited to 5
requests per minute per store host and email address, with a 30-per-
minute per-IP backstop.
Method Path Body
POST /api/customer/
login
email, password

## PDF page 31

POST /api/customer/
register
name, email, password, optional
phone, gender, dob, note,
address
POST /api/customer/
verify-
registration
email, otp (6 characters)
POST /api/customer/
resend-
registration-
otp
email
POST /api/customer/
forgot-password
email
POST /api/customer/
reset-password
email, otp, password,
password_confirmation
POST /api/customer/
change-password
current_password, password,
password_confirmation
POST /api/customer/
logout
—
POST /api/customer/
my-account
Empty body reads the proﬁle;
sending any of name, phone,
gender, dob, note, address
updates it.
GET /api/customer/
my-orders
Query: page, per_page (max 100)
Registration is a two-step ﬂow. register always returns the same
non-committal response ({ "pending_verification": true })
regardless of whether the email is new, belongs to an existing POS
customer, or already has an online account — it never reveals which.
The password is only staged; the account is not usable until verify-
registration succeeds with the emailed OTP, which also logs the
customer in.
login returns 422 with a uniform message on any failure, deliberately
not distinguishing a wrong password from an unknown email. change-

## PDF page 32

password, my-account and my-orders return 401 when there is no
session customer.
Checkout
GET /pre-checkout builds an encrypted payload from the current
basket and redirects to /checkout. On failure it redirects back to /
cart?checkout_error={message}. Link to it directly rather than
calling it with fetch:
<a href="/pre-checkout" class="btn-checkout">Checkout</a>
Rate limits
Page rendering is limited to 300 requests per minute per host and IP.
Asset requests are not rate limited.
13. Escaping and security
Liquid does not escape output. {{ value }} writes raw bytes into
the page. Two conventions keep this safe:
Visitor-supplied values are pre-escaped by the backend.
filters.*, request.query, and customer.* are HTML-escaped
as they enter the payload, so forgetting | escape on a search term is a
cosmetic bug rather than a scripting hole. Because escape is
idempotent here, adding it anyway is free and is the recommended
habit.
Catalogue copy is deliberately not escaped.
product.description, category.description and similar ﬁelds
are staﬀ-authored and may intentionally contain markup. Render them
raw where you want that HTML, and pipe through escape (or
strip_html) where you do not:

## PDF page 33

<div class="description">{{ product.description }}</div>
<meta name="description" content="{{ product.description | strip_html | truncate: 160 | escape }}">
In attributes, always escape:
<img src="{{ product.image_url }}" alt="{{ product.name | escape }}">
In JavaScript, never interpolate a string directly into a script body. Use
variants_json where it exists, or a data attribute:
<script>window.PRODUCT_VARIANTS = {{ product.variants_json }};</script>
<div id="app" data-product-name="{{ product.name | escape }}"></div>
Themes are tenant-authored code running on the tenant's own origin, so
the platform's guarantees are about containment: path traversal is
blocked in both the template ﬁlesystem and the page resolver, only inert
asset types are publicly servable, and cross-origin writes are refused.
14. Resource limits and error behaviour
Every render is bounded. Exceeding any limit aborts the page with a 500
and a logged error naming the theme and path.
Limit Value
Render output length 5 MB
Render score 200,000
Assign score 5 MB
Cumulative render score 1,000,000
Cumulative assign score 20 MB
In practice these are only reached by a runaway loop or output far past

## PDF page 34

any real page size. Watch for unbounded {% for %} over a large
collection and repeated {% capture %} of large strings.
Failure behaviour summary
Situation Result
Undeﬁned variable Renders as empty string.
Unknown ﬁlter Input passes through unchanged.
Missing partial Renders as empty string; a warning is
logged.
Page not in
pages.json, or draft in
prod
Browser is redirected to /. Home page and
JSON clients get a real 404.
Empty page template Treated as a missing page, not a blank
200.
Login required, not
logged in
Redirect to /login?
redirect={canonical path}.
Guest-only page,
logged in
Redirect to /account.
Resource limit
exceeded
Branded 500 error page.
Integration or module-
data provider throws
That contribution is dropped; the page
renders.
15. Extending the payload from PHP
Two extension points let backend modules reach a theme without
Builder knowing they exist. Both are for platform developers, not theme
authors, but knowing they exist explains where unfamiliar variables
come from.

## PDF page 35

StorefrontDataProvider contributes a top-level key to the Liquid
payload. A module implements the interface and registers it from its own
service provider:
// in SomeModuleServiceProvider::boot()
$this->callAfterResolving(
    StorefrontDataRegistry::class,
    fn (StorefrontDataRegistry $registry) => $registry->register(new LoyaltyStorefrontData),
);
Whatever provide() returns lands under key(), so the theme reads
{{ loyalty.points_balance }}. Keys must be valid Liquid
identiﬁers and cannot be one of the reserved names in section 6.14.
appliesTo() keeps a module's queries oﬀ pages that never read
them, and any string a provider derives from visitor input must be
escaped with App\Modules\Builder\Support\Escaper.
Integration contributes markup at one of the {% content_for_*
%} injection points, plus optional data under its own key. Use
StoreContext::isLive() to keep builder-preview traﬃc out of
tenant analytics.
16. Quick reference
Payload keys at a glance
environment          'dev' | 'prod'
request              { path, query }
csrf_token           string
store                { id, name, tenant_id, maintenance_mode, is_guest_checkout }
theme                { id, store_id, slug, asset_base }
page                 { title, slug, page, seo_*, og_* }
products             { items[], pagination }
product              detailed product (product pages only)
categories           { items[], pagination }

## PDF page 36

category             single category (category pages only)
filters              { search, sort, category, min_price, max_price, per_page }
filter_categories    []
filter_price_range   { min, max }
basket               { items[], item_count, sub_total, total, … } or nil
customer             { id, name, email, phone } or nil
auth_check           bool
settings             defaults.json contents
menu                 { items[] }
FlowPOS-speciﬁc syntax
{% content_for_header %}
{% content_for_body %}
{% content_for_footer %}
{{ 'css/theme.css' | asset_url }}
{{ 12.5 | money }}
{% assign picks = "slug-a,slug-b" | split: "," | get_products %}
Common mistakes
•  Expecting a layout tag. There is none. Each page renders its
own full document, conventionally via layout-start / layout-
end partials.
•  Using {% include %}. It does not exist. Use {% render %}.
•  Assuming {% render %} sees page globals. It does not. Pass
what the partial needs.
•  Treating catalogue booleans as true/false. They are 1/0
integers.
•  Not guarding basket and customer. Both are nil for an
anonymous visitor with no basket.
•  Naming the settings ﬁle default.json. It must be
defaults.json at the theme root, or {{ settings }} is
empty.
•  Sending a write without the CSRF token. Every POST/PUT to

## PDF page 37

the storefront API needs X-CSRF-TOKEN.
•  Treating PUT /api/basket as "add to basket". It replaces the
whole item array.
•  Expecting a missing partial to raise. It renders empty and logs
— check the log.
•  Relying on escape to double-encode. It does not; it is
idempotent here.

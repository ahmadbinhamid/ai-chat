--- Redesign brief ---
This turn is a redesign: the merchant expects a visible, substantial change. A result that only changes fonts, sizes or colours has failed. Rebuild page markup and CSS; write a new component where no §8 one fits the new design; use action "update" for files you rebuild.

Keep what works:
- Keep every data-* hook spelt identically — the JS finds elements by them, and a renamed or dropped hook breaks cart, menus and forms.
- Keep the content and features the merchant asked for in earlier turns.
- Never invent products, prices, reviews or testimonials. Featured products come from real data: existing product components, or get_products with slugs the theme already uses.

Design principles:
- Clear hierarchy: one strong hero with a large headline, a supporting line, a primary and a secondary CTA, and real imagery.
- A consistent spacing scale with generous section spacing; a type scale with clear contrast between display and body.
- A cohesive palette from the brand colour: tints and shades via color-mix() on var(--theme-*) tokens, plus one accent.
- When the merchant names a brand colour or font, set it in defaults.json (change the existing colors/font values; never restructure keys), then build tints and shades from those tokens.
- Cards with depth (radius, subtle shadow) and visible hover/focus states.
- Mobile-first responsive layout; subtle CSS-only motion, off under prefers-reduced-motion.
- Logo: an inline SVG mark beside {{ store.name }} — never an invented image file.

Homepage, unless the merchant listed the sections: hero; value props/features; featured products; story/about; testimonials only if real ones exist; a call to action; a rich footer.

"A hero on every page": restyle the shared page-hero in pages/css/page-shared.css once, then add the page-hero markup to every page that lacks one (using the shared classes, page-specific heading and copy). Never copy the hero CSS into each page.

Imagery, in this order:
1. Images the merchant attached, placed with use_attachments.
2. Photos from search_stock_images, when offered: hero and section images matching the brand, saved into images/ with use_attachments (stock_image: id).
3. Images already in the theme, via asset_url.
4. Inline SVG or CSS shapes.
Every <img> gets alt text, width and height, and responsive sizing (sizes, max-width: 100%). Never link an external image or invent a URL.

Every hard rule still applies: tokens instead of raw colours, §1 Liquid only, §7 fields only, registered CSS/JS, no frameworks, no placeholder text.

Before you call propose_changes, check:
- Every data-* hook from the old markup is still there, spelt identically.
- The change is visible at a glance, not just fonts, sizes or colours.
- Every new CSS/JS file is registered; every colour is a token or a declared custom property.
- No invented products, prices, reviews, testimonials or image URLs; no placeholder text.
- Earlier requested content and features are still there.

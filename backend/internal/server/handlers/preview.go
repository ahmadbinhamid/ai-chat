package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"time"

	"ai-chat/internal/auth"
	"ai-chat/internal/httpresponse"
	"ai-chat/internal/liquidrender"
	"ai-chat/internal/modules/themebuild"
	"ai-chat/internal/themefs"

	"github.com/gin-gonic/gin"
)

// defaultProductsFetchTimeout mirrors FLOWPOS_HTTP_TIMEOUT_MS's own default
// (2000ms, see internal/config) — used only until server.go's wiring calls
// SetProductsFetchTimeout with the real configured value; kept as a sane
// standalone default so a PreviewHandler built without that call (every
// existing test in this package) still bounds the fetch instead of
// inheriting *themefs.Store's much longer 60s client-wide timeout.
const defaultProductsFetchTimeout = 2 * time.Second

// buildPreviewContext is themefs.FixtureContext() with the tenant's real
// store identity, real nav menu, and real published products overlaid on
// top (see themebuild.Service.FetchStoreSettings / FetchThemeMenu /
// FetchPreviewProducts) — category/categories/basket/customer stay fixture
// data: those have no single real value the way a store's name/menu/
// products do, and fetching a real basket/customer adds auth complexity a
// preview has no need for. product (the detail-page singular) also stays
// fixture, deliberately — see previewProductItems' doc comment on why the
// products-LIST endpoint's data can't safely stand in for it.
//
// Shared by Preview and Context rather than each calling FixtureContext()
// independently: the frontend's LiquidJS engine (tenant-dashboard's
// liquid-engine.ts) renders against Context's output, and PreviewPane's
// "Check accuracy" button diffs that against Preview's own render — if only
// one of the two carried the real store name/menu/products, accuracy-check
// would report a false "these differ" on every single page, which is
// exactly the noise it exists to avoid (see liquid-engine.ts's own comment
// on what it's actually meant to catch).
//
// Each overlay falls back to the fixture value on its own fetch failure —
// one lookup hiccup (e.g. a brand-new theme with no defaults.json yet, or a
// merchant with zero products) shouldn't block another overlay or break the
// whole preview render. Sequential, not concurrent: three fetches add real
// latency to every preview load, but this stays a request-scoped call on an
// endpoint with no tight latency budget, and sequential keeps every
// fail-open branch as simple as the two that already existed.
func buildPreviewContext(ctx context.Context, builder *themebuild.Service, storeAuth themefs.RequestAuth, productsFetchTimeout time.Duration) map[string]any {
	fixture := themefs.FixtureContext()

	if settings, err := builder.FetchStoreSettings(ctx, storeAuth); err == nil && settings.Name != "" {
		fixture["store"] = map[string]any{"name": settings.Name}
	}
	if menu, err := builder.FetchThemeMenu(ctx, storeAuth); err == nil {
		fixture["menu"] = menu
	}

	// FixtureProducts()' own item count is the cap on how many real
	// products are ever fetched — a merchant with a huge catalogue must not
	// blow up this response, and the preview never needed to show more than
	// the fixture already does. Read from the fixture itself (not a
	// separately hardcoded number) so the two can never quietly drift.
	limit := len(fixture["products"].(map[string]any)["items"].([]any))

	fetchCtx, cancel := context.WithTimeout(ctx, productsFetchTimeout)
	defer cancel()
	page, err := builder.FetchPreviewProducts(fetchCtx, storeAuth, limit)
	if err == nil && len(page.Items) > 0 {
		fixture["products"] = map[string]any{
			"items":      previewProductItems(page.Items),
			"pagination": previewProductsPagination(len(page.Items)),
		}
		slog.Info("preview: products source", "source", "real", "count", len(page.Items))
	} else {
		// Covers every fail-open case the same way: a fetch error, a
		// timeout (context.DeadlineExceeded satisfies err == nil's
		// negation the same as any other error), and a merchant with a
		// genuinely empty catalogue (err == nil but zero items) — all keep
		// the fixture rather than rendering a blank shop page a merchant
		// could mistake for a broken theme.
		slog.Info("preview: products source", "source", "fixture", "count", limit)
	}

	return fixture
}

// previewProductItems maps flowpos-backend's real products-list response
// into theme_engine_spec.md §7's products.items[] shape. Deliberately
// missing choices[]/full variants[] (left as empty/false, not omitted, so
// the key set matches the fixture's even when the value can't be real): the
// products-LIST endpoint (GET /products) doesn't eager-load what those
// need — that's only ever loaded by the single-product GET
// /products/{slug} endpoint. This is exactly why buildPreviewContext keeps
// `product` (the detail-page singular) on the fixture instead of reusing
// items[0] for it — substituting a list item there would be real data that
// is structurally wrong for a detail page's own markup (variant/choice
// selectors), which is worse than honest fixture data. Every field the
// components that actually render a list item (product-grid-block,
// product-list-item — see spec §7's component table) reference IS real
// here: name, image_url, price_formatted, has_variants, default_variant_id,
// url, slug.
func previewProductItems(products []themefs.Product) []any {
	items := make([]any, len(products))
	for i, p := range products {
		items[i] = previewProductItem(p)
	}
	return items
}

func previewProductItem(p themefs.Product) map[string]any {
	imageURL := ""
	images := make([]any, 0, len(p.Attachments))
	for _, a := range p.Attachments {
		images = append(images, map[string]any{"url": a.URL})
		if imageURL == "" {
			imageURL = a.URL
		}
	}

	onSale := p.ComparePrice != nil && *p.ComparePrice > p.Price
	compareFormatted := ""
	if onSale {
		compareFormatted = formatGBP(*p.ComparePrice)
	}
	defaultVariantID := ""
	if p.DefaultVariant != nil {
		defaultVariantID = strconv.Itoa(p.DefaultVariant.ID)
	}

	return map[string]any{
		"name":                       p.Name,
		"id":                         strconv.Itoa(p.ID),
		"slug":                       p.Slug,
		"sku":                        stringOr(p.SKU, ""),
		"barcode":                    stringOr(p.Barcode, ""),
		"description":                p.Description,
		"image_url":                  imageURL,
		"images":                     images,
		"price_formatted":            formatGBP(p.Price),
		"price_amount":               priceAmountPence(p.Price),
		"compare_at_price_formatted": compareFormatted,
		"on_sale":                    onSale,
		// Not eager-loaded by the list endpoint — see this function's own
		// doc comment. false/empty, not omitted: a page checking
		// product.has_choices renders its "no choices" branch, exactly the
		// same as a real product with none.
		"has_choices":        false,
		"choices":            []any{},
		"has_variants":       p.HasVariants,
		"variants":           []any{},
		"default_variant_id": defaultVariantID,
		"variants_json":      "[]",
		"url":                "/product/" + p.Slug,
	}
}

// previewProductsPagination reports a single, non-navigable page — always
// "page 1 of 1", has_prev/has_next both false, matching what's actually in
// items (see previewProductItems' own doc comment on why only one page is
// ever fetched at all). This used to report the REAL current/last page
// flowpos-backend's paginator returns, on the theory that an accurate
// has_next mattered more than pretending there's nothing more to see — in
// practice that was worse: the client-side draft preview has no mechanism
// to actually fetch or render a second page, so a real has_next just drew a
// live-looking "Next" button that silently did nothing when clicked (a
// merchant reported this directly). A pagination control the theme can't
// act on is worse than one that's honest there's only one page here.
func previewProductsPagination(itemCount int) map[string]any {
	return map[string]any{
		"page": 1, "last_page": 1, "total": itemCount, "per_page": itemCount,
		"has_prev": false, "has_next": false, "prev_page": nil, "next_page": nil,
	}
}

// formatGBP renders amount as the platform's one supported currency —
// GBP-only across every theme and tenant today (see e.g. the theme's own
// js/storefront-api.js EcommerceTracking comment: "Currency: GBP
// (platform GBP-only)") — so a fixed "£" prefix is correct, not a
// simplification that happens to work for one tenant.
func formatGBP(amount float64) string {
	return fmt.Sprintf("£%.2f", amount)
}

// priceAmountPence converts a pound-and-pence float into the integer pence
// FixtureProduct's own price_amount uses (e.g. 19.99 -> 1999).
func priceAmountPence(amount float64) int {
	return int(math.Round(amount * 100))
}

func stringOr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}

// PreviewHandler renders a theme page against fixture data (see
// themefs.FixtureContext) — a merchant can see what a page looks like
// without it ever being saved to the real theme. The frontend's own
// LiquidJS engine (see tenant-dashboard's liquid-engine.ts) now does this
// same rendering client-side, instantly, for the AI chat page's draft
// preview — this endpoint stays as the fidelity reference it renders
// against ("Check accuracy" in PreviewPane.tsx posts the draft here and
// diffs the two HTML outputs), not something replaced by the client-side
// engine. Do not delete this or internal/liquidrender.
type PreviewHandler struct {
	builder              *themebuild.Service
	productsFetchTimeout time.Duration
}

func NewPreviewHandler(builder *themebuild.Service) *PreviewHandler {
	return &PreviewHandler{builder: builder, productsFetchTimeout: defaultProductsFetchTimeout}
}

// SetProductsFetchTimeout overrides the default products-fetch timeout with
// FLOWPOS_HTTP_TIMEOUT_MS's real configured value — not a NewPreviewHandler
// parameter so every existing caller (including every test in this package)
// keeps compiling unchanged; mirrors themebuild.Service's own
// SetHistorySummarizationEnabled for the same reason. Call once, before
// serving traffic.
func (h *PreviewHandler) SetProductsFetchTimeout(d time.Duration) {
	h.productsFetchTimeout = d
}

type previewRequest struct {
	// Page is a pages.json basename (e.g. "home") — resolves to
	// pages/home.liquid. Ignored if Path is set.
	Page string `json:"page"`
	// Path is an explicit theme-relative path override, e.g.
	// "pages/auth/login.liquid" (for a page under pages/auth/).
	Path string `json:"path"`
	// Files, if set, overlays these theme-relative paths -> content on top
	// of the real theme's own files before rendering — an unsaved draft
	// (potentially many files: pages, components, layout splices), exactly
	// the case a preview/accuracy-check exists for. Superset of the old
	// single-file Content field: a draft is never just one file once a
	// chat's turn touches a component another page also renders.
	Files map[string]string `json:"files"`
}

type previewResponse struct {
	HTML   string   `json:"html"`
	Errors []string `json:"errors"`
}

// Preview handles POST /api/v1/themes/:slug/preview. :slug is accepted for
// API shape/future use but doesn't currently select among multiple themes
// — every themefs.Store call (see its own doc comment) always operates on
// the caller's one active theme, resolved server-side from the tenant, the
// same way every other route in this service already works; ai-chat has
// no notion of a non-active theme to preview instead.
func (h *PreviewHandler) Preview(c *gin.Context) {
	var in previewRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		respondBindErr(c, err)
		return
	}

	entryPath := in.Path
	if entryPath == "" && in.Page != "" {
		entryPath = "pages/" + in.Page + ".liquid"
	}
	if entryPath == "" {
		respondBindErr(c, errors.New("one of page or path is required"))
		return
	}

	storeAuth := themefs.RequestAuth{Token: auth.Token(c), TenantID: auth.TenantID(c)}
	files, err := h.builder.LoadBaseThemeFiles(c.Request.Context(), storeAuth, false)
	if err != nil {
		respondErr(c, err)
		return
	}
	for path, content := range in.Files {
		files[path] = content
	}

	renderer := liquidrender.Renderer{Files: files}
	html, errs := renderer.Render(entryPath, buildPreviewContext(c.Request.Context(), h.builder, storeAuth, h.productsFetchTimeout))
	if errs == nil {
		errs = []string{}
	}

	httpresponse.OK(c, previewResponse{HTML: html, Errors: errs})
}

// Context handles GET /api/v1/preview/context — themefs.FixtureContext()
// as JSON, so the frontend's LiquidJS preview never hand-copies the
// fixture data into TypeScript (a second, driftable copy of the same
// sample product/category/etc. shape this Go endpoint already owns) and
// instead fetches the one source of truth.
func (h *PreviewHandler) Context(c *gin.Context) {
	storeAuth := themefs.RequestAuth{Token: auth.Token(c), TenantID: auth.TenantID(c)}
	httpresponse.OK(c, buildPreviewContext(c.Request.Context(), h.builder, storeAuth, h.productsFetchTimeout))
}

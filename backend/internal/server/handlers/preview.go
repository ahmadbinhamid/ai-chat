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

// defaultProductsFetchTimeout mirrors FLOWPOS_HTTP_TIMEOUT_MS's default (2000ms), used until SetProductsFetchTimeout sets the real value.
// Keeps a PreviewHandler built without that call (e.g. tests) from inheriting themefs.Store's much longer 60s timeout.
const defaultProductsFetchTimeout = 2 * time.Second

// buildPreviewContext overlays real store name/menu/products on the fixture; category/basket/custom...
func buildPreviewContext(ctx context.Context, builder *themebuild.Service, storeAuth themefs.RequestAuth, productsFetchTimeout time.Duration) map[string]any {
	fixture := themefs.FixtureContext()

	if settings, err := builder.FetchStoreSettings(ctx, storeAuth); err == nil && settings.Name != "" {
		fixture["store"] = map[string]any{"name": settings.Name}
	}
	if menu, err := builder.FetchThemeMenu(ctx, storeAuth); err == nil {
		fixture["menu"] = menu
	}

	// Capped to FixtureProducts()' own item count (read from the fixture so the two can't drift), bounding a huge catalogue's response.
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
		// Covers fetch error, timeout, and a genuinely empty catalogue alike — keep the fixture rather than a blank shop page.
		slog.Info("preview: products source", "source", "fixture", "count", limit)
	}

	return fixture
}

// previewProductItems maps the real products-list response into theme_engine_spec.md §7's shape. ch...
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
		// Not eager-loaded by the list endpoint; false/empty (not omitted) so a "no choices" branch still renders correctly.
		"has_choices":        false,
		"choices":            []any{},
		"has_variants":       p.HasVariants,
		"variants":           []any{},
		"default_variant_id": defaultVariantID,
		"variants_json":      "[]",
		"url":                "/product/" + p.Slug,
	}
}

// previewProductsPagination always reports a single, non-navigable page (has_next false): the draft preview can't fetch or
// render a second page, so a real has_next would draw a "Next" button that silently does nothing when clicked.
func previewProductsPagination(itemCount int) map[string]any {
	return map[string]any{
		"page": 1, "last_page": 1, "total": itemCount, "per_page": itemCount,
		"has_prev": false, "has_next": false, "prev_page": nil, "next_page": nil,
	}
}

// formatGBP assumes GBP, the platform's only supported currency across every theme and tenant today.
func formatGBP(amount float64) string {
	return fmt.Sprintf("£%.2f", amount)
}

// priceAmountPence converts pounds-and-pence to the integer pence FixtureProduct's price_amount uses (e.g. 19.99 -> 1999).
func priceAmountPence(amount float64) int {
	return int(math.Round(amount * 100))
}

func stringOr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}

// PreviewHandler renders a theme page against fixture data so a merchant can preview without saving to the real theme.
// The frontend's LiquidJS engine now renders client-side, but this stays PreviewPane's accuracy-check reference — do not delete this or internal/liquidrender.
type PreviewHandler struct {
	builder              *themebuild.Service
	productsFetchTimeout time.Duration
}

func NewPreviewHandler(builder *themebuild.Service) *PreviewHandler {
	return &PreviewHandler{builder: builder, productsFetchTimeout: defaultProductsFetchTimeout}
}

// SetProductsFetchTimeout overrides the default with FLOWPOS_HTTP_TIMEOUT_MS's real value; not a constructor
// param so existing callers/tests keep compiling unchanged. Call once, before serving traffic.
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
	// Files overlays theme-relative path -> content on top of the real theme before rendering — an unsaved,
	// possibly multi-file draft (a chat turn can touch a component shared by other pages).
	Files map[string]string `json:"files"`
}

type previewResponse struct {
	HTML   string   `json:"html"`
	Errors []string `json:"errors"`
}

// Preview handles POST /api/v1/themes/:slug/preview. :slug is accepted for API shape but unused — every call operates on the tenant's one active theme.
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

// Context handles GET /api/v1/preview/context — returns themefs.FixtureContext() as JSON so the frontend fetches the one source of truth instead of hand-copying a driftable second copy.
func (h *PreviewHandler) Context(c *gin.Context) {
	storeAuth := themefs.RequestAuth{Token: auth.Token(c), TenantID: auth.TenantID(c)}
	httpresponse.OK(c, buildPreviewContext(c.Request.Context(), h.builder, storeAuth, h.productsFetchTimeout))
}

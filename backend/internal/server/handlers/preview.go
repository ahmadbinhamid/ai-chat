package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"strings"
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

	fetchCtx, cancel := context.WithTimeout(ctx, productsFetchTimeout)
	defer cancel()
	page, err := builder.FetchPreviewProducts(fetchCtx, storeAuth, previewProductLimit)
	if err != nil || len(page.Items) == 0 {
		// Covers fetch error, timeout, and a genuinely empty catalogue alike — keep the fixture rather than a blank shop page.
		slog.Info("preview: products source", "source", "fixture", "error", err)
		return fixture
	}
	fixture["products"] = map[string]any{
		"items":      previewProductItems(page.Items),
		"pagination": previewProductsPagination(len(page.Items)),
	}
	slog.Info("preview: products source", "source", "real", "count", len(page.Items))

	// Own timeout, started after the list returns, so a slow list can't starve the detail fetch or vice versa.
	detailCtx, detailCancel := context.WithTimeout(ctx, productsFetchTimeout)
	defer detailCancel()
	detail, err := builder.FetchPreviewProductDetail(detailCtx, storeAuth, page.Items[0].Slug)
	if err != nil {
		slog.Info("preview: product detail source", "source", "fixture", "error", err)
		return fixture
	}
	fixture["product"] = previewProductDetail(detail)
	slog.Info("preview: product detail source", "source", "real", "slug", detail.Slug)

	return fixture
}

// previewProductLimit is how many real products the preview's grids show; the fallback fixture keeps its own smaller list.
const previewProductLimit = 6

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
	// §7: addable straight from a listing only when there's no option to pick and a variant to add.
	quickAdd := 0
	if !p.HasVariants && p.DefaultVariant != nil {
		quickAdd = 1
	}

	return map[string]any{
		"name":                       p.Name,
		"title":                      p.Name,
		"id":                         strconv.Itoa(p.ID),
		"slug":                       p.Slug,
		"sku":                        stringOr(p.SKU, ""),
		"barcode":                    stringOr(p.Barcode, ""),
		"description":                p.Description,
		"image_url":                  imageURL,
		"images":                     images,
		"price_formatted":            formatGBP(p.Price),
		"price_amount":               p.Price,
		"compare_at_price_formatted": compareFormatted,
		"on_sale":                    onSale,
		// Not eager-loaded by the list endpoint; false/empty (not omitted) so a "no choices" branch still renders correctly.
		"has_choices":        false,
		"choices":            []any{},
		"has_variants":       p.HasVariants,
		"can_quick_add":      quickAdd,
		"show_add_to_cart":   quickAdd,
		"variants":           []any{},
		"default_variant_id": defaultVariantID,
		"variants_json":      "[]",
		"url":                "/product/" + p.Slug,
	}
}

// previewProductDetail maps GET /products/{slug} into §7's product-page shape, mirroring flowpos-backend's storefront
// DataService::mapProduct(detailed) field by field so preview and live agree; only §7's fields are emitted.
func previewProductDetail(p themefs.ProductDetail) map[string]any {
	price := p.Price
	if p.DefaultVariant != nil {
		price = p.DefaultVariant.Price
	}
	for i, v := range p.Variants {
		if i == 0 || v.Price < price {
			price = v.Price
		}
	}
	onSale := p.ComparePrice != nil && *p.ComparePrice > price
	compareFormatted := ""
	if onSale {
		compareFormatted = formatGBP(*p.ComparePrice)
	}

	imageURL := ""
	if len(p.Attachments) > 0 {
		imageURL = p.Attachments[0].URL
	} else if p.DefaultVariant != nil && len(p.DefaultVariant.Attachments) > 0 {
		imageURL = p.DefaultVariant.Attachments[0].URL
	}
	images := attachmentImages(p.Attachments)
	if len(images) == 0 && imageURL != "" {
		images = []any{map[string]any{"url": imageURL}}
	}

	hasVariants := p.VariantsCount > 0 || len(p.Variants) > 0
	var defaultVariantID any
	barcode := derefString(p.Barcode)
	if p.DefaultVariant != nil {
		defaultVariantID = p.DefaultVariant.ID
		if p.DefaultVariant.Barcode != nil {
			barcode = *p.DefaultVariant.Barcode
		}
	}
	quickAdd := 0
	if !hasVariants && p.DefaultVariant != nil {
		quickAdd = 1
	}

	variants := make([]any, 0, len(p.Variants))
	for _, v := range p.Variants {
		variants = append(variants, previewVariant(v))
	}
	// Marshal can't fail on these plain map/slice/string/number values.
	variantsJSON, _ := json.Marshal(variants)

	groupVariants := p.Variants
	if p.DefaultVariant != nil {
		groupVariants = append([]themefs.ProductVariant{*p.DefaultVariant}, p.Variants...)
	}
	addonGroups := previewAddonGroups(groupVariants)
	choices := previewChoices(p.Variants)

	return map[string]any{
		"id":                         p.ID,
		"name":                       p.Name,
		"title":                      p.Name,
		"slug":                       p.Slug,
		"url":                        "/product/" + url.PathEscape(p.Slug),
		"image_url":                  imageURL,
		"images":                     images,
		"price_amount":               price,
		"price_formatted":            formatGBP(price),
		"compare_at_price_formatted": compareFormatted,
		"on_sale":                    boolInt(onSale),
		"sku":                        derefString(p.SKU),
		"barcode":                    barcode,
		"description":                p.Description,
		"default_variant_id":         defaultVariantID,
		"has_variants":               boolInt(hasVariants),
		"can_quick_add":              quickAdd,
		"show_add_to_cart":           quickAdd,
		"variants":                   variants,
		"variant_count":              len(variants),
		"variants_json":              string(variantsJSON),
		"choices":                    choices,
		"has_choices":                boolInt(len(choices) > 0),
		"addon_groups":               addonGroups,
		"add_on_groups":              addonGroups,
		"addons":                     flattenAddonGroups(addonGroups),
		"has_addons":                 boolInt(len(addonGroups) > 0),
	}
}

func previewVariant(v themefs.ProductVariant) map[string]any {
	var names []string
	options := map[string]any{}
	for _, item := range v.Items {
		if name := strings.TrimSpace(item.Name); name != "" {
			names = append(names, name)
		}
		if typeID := choiceTypeID(item); typeID > 0 {
			options[strconv.Itoa(typeID)] = item.ID
		}
	}
	label := strings.Join(names, " / ")
	if label == "" {
		label = strings.TrimSpace(derefString(v.SKU))
	}
	if label == "" {
		label = "Option #" + strconv.Itoa(v.ID)
	}
	images := attachmentImages(v.Attachments)
	imageURL := ""
	if len(images) > 0 {
		imageURL = images[0].(map[string]any)["url"].(string)
	}
	return map[string]any{
		"id":              v.ID,
		"label":           label,
		"sku":             derefString(v.SKU),
		"barcode":         derefString(v.Barcode),
		"price_amount":    v.Price,
		"price_formatted": formatGBP(v.Price),
		"is_available":    boolInt(v.IsAvailable == nil || *v.IsAvailable),
		"image_url":       imageURL,
		"images":          images,
		"options":         options,
	}
}

// previewChoices groups variant choice items by choice type, first-seen order, each item once.
func previewChoices(variants []themefs.ProductVariant) []any {
	var order []int
	byType := map[int]map[string]any{}
	seenItem := map[int]map[int]bool{}
	for _, v := range variants {
		for _, item := range v.Items {
			typeID := choiceTypeID(item)
			if typeID <= 0 {
				continue
			}
			if byType[typeID] == nil {
				label := "Option"
				if item.ChoiceType != nil && item.ChoiceType.Label != "" {
					label = item.ChoiceType.Label
				}
				byType[typeID] = map[string]any{"id": typeID, "label": label, "items": []any{}}
				seenItem[typeID] = map[int]bool{}
				order = append(order, typeID)
			}
			if !seenItem[typeID][item.ID] {
				seenItem[typeID][item.ID] = true
				byType[typeID]["items"] = append(byType[typeID]["items"].([]any), map[string]any{"id": item.ID, "name": item.Name})
			}
		}
	}
	choices := make([]any, len(order))
	for i, id := range order {
		choices[i] = byType[id]
	}
	return choices
}

func choiceTypeID(item themefs.ProductChoiceItem) int {
	if item.ChoiceType != nil && item.ChoiceType.ID > 0 {
		return item.ChoiceType.ID
	}
	return item.CtypeID
}

// previewAddonGroups dedupes groups by id in first-seen order, keeping only active groups with active add-ons, as live does.
func previewAddonGroups(variants []themefs.ProductVariant) []any {
	var groups []any
	seen := map[int]bool{}
	for _, v := range variants {
		for _, g := range v.AddOnGroups {
			if seen[g.ID] || (g.IsActive != nil && !*g.IsActive) {
				continue
			}
			addons := make([]themefs.AddOn, 0, len(g.Addons))
			for _, a := range g.Addons {
				if a.IsActive == nil || *a.IsActive {
					addons = append(addons, a)
				}
			}
			if len(addons) == 0 {
				continue
			}
			seen[g.ID] = true
			sort.SliceStable(addons, func(i, j int) bool { return addons[i].SortOrder < addons[j].SortOrder })
			mapped := make([]any, len(addons))
			for i, a := range addons {
				maxQty := 1
				if a.MaxQuantity != nil {
					maxQty = *a.MaxQuantity
				}
				mapped[i] = map[string]any{
					"id": a.ID, "name": a.Name, "price_amount": a.Price, "price_formatted": formatGBP(a.Price),
					"max_quantity": maxQty, "is_active": 1,
				}
			}
			groups = append(groups, map[string]any{
				"id": g.ID, "name": g.Name, "min_selection": g.MinSelection, "max_selection": g.MaxSelection,
				"is_required": boolInt(g.MinSelection > 0), "addons": mapped,
			})
		}
	}
	if groups == nil {
		return []any{}
	}
	return groups
}

// flattenAddonGroups builds §7's flat addons[] from the groups, each add-on once, carrying its group id and name.
func flattenAddonGroups(groups []any) []any {
	flat := []any{}
	seen := map[int]bool{}
	for _, g := range groups {
		group := g.(map[string]any)
		for _, a := range group["addons"].([]any) {
			addon := a.(map[string]any)
			if seen[addon["id"].(int)] {
				continue
			}
			seen[addon["id"].(int)] = true
			withGroup := map[string]any{"group_id": group["id"], "group_name": group["name"]}
			for k, v := range addon {
				withGroup[k] = v
			}
			flat = append(flat, withGroup)
		}
	}
	return flat
}

func attachmentImages(attachments []themefs.ProductAttachment) []any {
	images := []any{}
	for _, a := range attachments {
		if a.URL != "" {
			images = append(images, map[string]any{"url": a.URL})
		}
	}
	return images
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// previewProductsPagination always reports a single, non-navigable page (has_next false): the draft preview can't fetch or
// render a second page, so a real has_next would draw a "Next" button that silently does nothing when clicked.
func previewProductsPagination(itemCount int) map[string]any {
	return map[string]any{
		"page": 1, "last_page": 1, "total": itemCount, "per_page": 15,
		"has_prev": false, "has_next": false, "prev_page": nil, "next_page": nil,
	}
}

// formatGBP assumes GBP, the platform's only supported currency across every theme and tenant today.
func formatGBP(amount float64) string {
	return fmt.Sprintf("£%.2f", amount)
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

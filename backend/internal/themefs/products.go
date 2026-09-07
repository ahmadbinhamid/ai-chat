package themefs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// Product is the subset of flowpos-backend's GET /products response (the
// same tenant-authenticated products-list endpoint tenant-dashboard's own
// product management screens call — ProductController::index) the theme
// engine's preview context needs. Field names and presence are confirmed
// against tenant-dashboard's own Product TS type
// (src/lib/api/products.ts), not just the raw Eloquent model, since that
// type reflects the shape already relied on in production. Price is the
// tenant's stored price as returned by that endpoint — the model also
// appends a computed, VAT-inclusive gross_price, but that field isn't part
// of the confirmed TS shape, so it's deliberately not depended on here.
type Product struct {
	ID             int                 `json:"id"`
	Slug           string              `json:"slug"`
	Name           string              `json:"name"`
	Description    string              `json:"description"`
	Price          float64             `json:"price"`
	ComparePrice   *float64            `json:"compare_price"`
	HasVariants    bool                `json:"has_variants"`
	SKU            *string             `json:"sku"`
	Barcode        *string             `json:"barcode"`
	Attachments    []ProductAttachment `json:"attachments"`
	DefaultVariant *ProductVariantRef  `json:"default_variant"`
}

// ProductAttachment is one of a product's images — URL is already the full,
// ready-to-use absolute URL (Attachment::getUrlAttribute() on the
// flowpos-backend side), not a relative path needing a base joined on.
type ProductAttachment struct {
	URL string `json:"url"`
}

// ProductVariantRef is the subset of a product's default variant this
// package needs — just enough to resolve default_variant_id for the theme
// engine's product-card "Add to Cart" branch (see spec §7's product-list-item
// component signature).
type ProductVariantRef struct {
	ID int `json:"id"`
}

// ProductsPage is one page of FetchProducts' result — Items capped at the
// requested limit (see FetchProducts), the rest describing the full
// catalogue on flowpos-backend's side so a caller can report accurate
// pagination even though only the first page was ever fetched.
type ProductsPage struct {
	Items       []Product
	CurrentPage int
	LastPage    int
	PerPage     int
	Total       int
}

type productsPageEnvelope struct {
	Data struct {
		Products struct {
			CurrentPage int       `json:"current_page"`
			Data        []Product `json:"data"`
			LastPage    int       `json:"last_page"`
			PerPage     int       `json:"per_page"`
			Total       int       `json:"total"`
		} `json:"products"`
	} `json:"data"`
}

// FetchProducts calls flowpos-backend's GET /products — the same
// tenant-authenticated, paginated products-list endpoint tenant-dashboard's
// own product management screens call (see src/lib/api/products.ts) —
// filtered to published, active products (what a real shopper would
// actually see on the live storefront) and capped at limit via that
// endpoint's own "limit" query param, which directly controls its Laravel
// paginate() page size server-side. Items is defensively re-capped at limit
// in case that ever isn't honoured. First page only — see this function's
// caller (PreviewHandler.buildPreviewContext) for why a full catalogue is
// never needed here.
func (s *Store) FetchProducts(ctx context.Context, auth RequestAuth, limit int) (ProductsPage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/products", nil)
	if err != nil {
		return ProductsPage{}, fmt.Errorf("build products request: %w", err)
	}
	q := req.URL.Query()
	q.Set("limit", strconv.Itoa(limit))
	q.Set("is_published_online", "1")
	q.Set("is_active", "1")
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	req.Header.Set("TID", strconv.FormatUint(auth.TenantID, 10))
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return ProductsPage{}, fmt.Errorf("fetch products: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ProductsPage{}, fmt.Errorf("fetch products: %s", statusErr(resp))
	}

	var out productsPageEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ProductsPage{}, fmt.Errorf("fetch products: decode response: %w", err)
	}

	items := out.Data.Products.Data
	if len(items) > limit {
		items = items[:limit]
	}
	return ProductsPage{
		Items:       items,
		CurrentPage: out.Data.Products.CurrentPage,
		LastPage:    out.Data.Products.LastPage,
		PerPage:     out.Data.Products.PerPage,
		Total:       out.Data.Products.Total,
	}, nil
}

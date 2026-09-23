package themefs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// Product is the subset of GET /products the preview context needs, confirmed against tenant-dashboard's Product TS type.
// Price is the tenant's stored price — the endpoint also returns a computed gross_price, deliberately not depended on here.
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

// ProductAttachment is one of a product's images — URL is already a full absolute URL, not a relative path.
type ProductAttachment struct {
	URL string `json:"url"`
}

// ProductVariantRef is just enough of a product's default variant to resolve default_variant_id for the "Add to Cart" branch.
type ProductVariantRef struct {
	ID int `json:"id"`
}

// ProductsPage is one page of FetchProducts' result — Items capped at limit, the rest describing the full catalogue for accurate pagination.
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

// FetchProducts calls GET /products filtered to published/active products, capped at limit via the query param.
// Items is defensively re-capped at limit in case that isn't honoured server-side. First page only.
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

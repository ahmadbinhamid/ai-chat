package themefs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
	query := url.Values{}
	query.Set("limit", strconv.Itoa(limit))
	query.Set("is_published_online", "1")
	query.Set("is_active", "1")
	var out productsPageEnvelope
	if err := s.getJSON(ctx, auth, "/products", query, &out); err != nil {
		return ProductsPage{}, fmt.Errorf("fetch products: %w", err)
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

// StorefrontPerPage is §7's page size: products/categories pagination and filters.per_page are always 15.
const StorefrontPerPage = 15

// FlattenAddonGroups builds §7's flat addons[] from addon_groups: each add-on once, carrying its group's id and name.
func FlattenAddonGroups(groups []any) []any {
	flat := []any{}
	seen := map[any]bool{}
	for _, g := range groups {
		group := g.(map[string]any)
		for _, a := range group["addons"].([]any) {
			addon := a.(map[string]any)
			if seen[addon["id"]] {
				continue
			}
			seen[addon["id"]] = true
			withGroup := map[string]any{"group_id": group["id"], "group_name": group["name"]}
			for k, v := range addon {
				withGroup[k] = v
			}
			flat = append(flat, withGroup)
		}
	}
	return flat
}

// ProductDetail is the subset of GET /products/{slug} the preview's product page needs. That admin endpoint loads each
// variant's add-on groups but not their add-ons, so AddOnGroup.Addons is filled separately via FetchAddOnGroup.
type ProductDetail struct {
	ID             int                 `json:"id"`
	Slug           string              `json:"slug"`
	Name           string              `json:"name"`
	Description    string              `json:"description"`
	Price          float64             `json:"price"`
	ComparePrice   *float64            `json:"compare_price"`
	SKU            *string             `json:"sku"`
	Barcode        *string             `json:"barcode"`
	VariantsCount  int                 `json:"variants_count"`
	Attachments    []ProductAttachment `json:"attachments"`
	DefaultVariant *ProductVariant     `json:"default_variant"`
	Variants       []ProductVariant    `json:"variants"`
}

// ProductVariant excludes the default variant: flowpos-backend's regular_variants scope keeps it out of Variants.
type ProductVariant struct {
	ID          int                 `json:"id"`
	SKU         *string             `json:"sku"`
	Barcode     *string             `json:"barcode"`
	Price       float64             `json:"price"`
	IsAvailable *bool               `json:"is_available"`
	Items       []ProductChoiceItem `json:"items"`
	Attachments []ProductAttachment `json:"attachments"`
	AddOnGroups []AddOnGroup        `json:"add_on_groups"`
}

type ProductChoiceItem struct {
	ID         int    `json:"id"`
	CtypeID    int    `json:"ctype_id"`
	Name       string `json:"name"`
	ChoiceType *struct {
		ID    int    `json:"id"`
		Label string `json:"label"`
	} `json:"choice_type"`
}

type AddOnGroup struct {
	ID           int     `json:"id"`
	Name         string  `json:"name"`
	IsActive     *bool   `json:"is_active"`
	MinSelection int     `json:"min_selection"`
	MaxSelection int     `json:"max_selection"`
	Addons       []AddOn `json:"addons"`
}

type AddOn struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	Price       float64 `json:"price"`
	MaxQuantity *int    `json:"max_quantity"`
	IsActive    *bool   `json:"is_active"`
	SortOrder   int     `json:"sort_order"`
}

// FetchProductDetail calls GET /products/{slug}.
func (s *Store) FetchProductDetail(ctx context.Context, auth RequestAuth, slug string) (ProductDetail, error) {
	var out struct {
		Data struct {
			Product ProductDetail `json:"product"`
		} `json:"data"`
	}
	if err := s.getJSON(ctx, auth, "/products/"+url.PathEscape(slug), nil, &out); err != nil {
		return ProductDetail{}, fmt.Errorf("fetch product detail: %w", err)
	}
	if out.Data.Product.ID == 0 {
		return ProductDetail{}, fmt.Errorf("fetch product detail: response has no product")
	}
	return out.Data.Product, nil
}

// FetchAddOnGroup calls GET /addon-groups/{id}, which, unlike the product endpoint, loads the group's add-ons.
func (s *Store) FetchAddOnGroup(ctx context.Context, auth RequestAuth, id int) (AddOnGroup, error) {
	var out struct {
		Data struct {
			AddOnGroup AddOnGroup `json:"addOnGroup"`
		} `json:"data"`
	}
	if err := s.getJSON(ctx, auth, "/addon-groups/"+strconv.Itoa(id), nil, &out); err != nil {
		return AddOnGroup{}, fmt.Errorf("fetch add-on group %d: %w", id, err)
	}
	return out.Data.AddOnGroup, nil
}

// getJSON is the shared authenticated GET to flowpos-backend: TID/bearer headers, non-200 as an error, JSON-decoded into out.
func (s *Store) getJSON(ctx context.Context, auth RequestAuth, path string, query url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if len(query) > 0 {
		req.URL.RawQuery = query.Encode()
	}
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	req.Header.Set("TID", strconv.FormatUint(auth.TenantID, 10))
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", statusErr(resp))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

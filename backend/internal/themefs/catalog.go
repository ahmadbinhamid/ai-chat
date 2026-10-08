package themefs

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// ProductsQuery is one filtered page of GET /products, in the tenant API's own terms (category by id, sort_by spelling).
type ProductsQuery struct {
	Limit      int
	Page       int
	Search     string
	SortBy     string
	CategoryID int
	MinPrice   *float64
	MaxPrice   *float64
}

// FetchProductsPage is FetchProducts with filters and a page; the visibility filters match the live storefront's.
func (s *Store) FetchProductsPage(ctx context.Context, auth RequestAuth, q ProductsQuery) (ProductsPage, error) {
	query := url.Values{}
	query.Set("limit", strconv.Itoa(q.Limit))
	query.Set("is_published_online", "1")
	query.Set("is_active", "1")
	if q.Page > 1 {
		query.Set("page", strconv.Itoa(q.Page))
	}
	if q.Search != "" {
		query.Set("search", q.Search)
	}
	if q.SortBy != "" {
		query.Set("sort_by", q.SortBy)
	}
	if q.CategoryID > 0 {
		query.Set("categories", strconv.Itoa(q.CategoryID))
	}
	if q.MinPrice != nil {
		query.Set("min_price", strconv.FormatFloat(*q.MinPrice, 'f', -1, 64))
	}
	if q.MaxPrice != nil {
		query.Set("max_price", strconv.FormatFloat(*q.MaxPrice, 'f', -1, 64))
	}
	var out productsPageEnvelope
	if err := s.getJSON(ctx, auth, "/products", query, &out); err != nil {
		return ProductsPage{}, fmt.Errorf("fetch products page: %w", err)
	}

	items := out.Data.Products.Data
	if len(items) > q.Limit {
		items = items[:q.Limit]
	}
	return ProductsPage{
		Items:       items,
		CurrentPage: out.Data.Products.CurrentPage,
		LastPage:    out.Data.Products.LastPage,
		PerPage:     out.Data.Products.PerPage,
		Total:       out.Data.Products.Total,
	}, nil
}

type Category struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	Description *string `json:"description"`
	Thumbnail   *struct {
		URL string `json:"url"`
	} `json:"thumbnail"`
}

// FetchCategories calls GET /categories for the published categories, newest first (the endpoint's only order), capped at limit.
func (s *Store) FetchCategories(ctx context.Context, auth RequestAuth, limit int) ([]Category, error) {
	query := url.Values{}
	query.Set("limit", strconv.Itoa(limit))
	query.Set("is_published_online", "1")
	var out struct {
		Data struct {
			Categories struct {
				Data []Category `json:"data"`
			} `json:"categories"`
		} `json:"data"`
	}
	if err := s.getJSON(ctx, auth, "/categories", query, &out); err != nil {
		return nil, fmt.Errorf("fetch categories: %w", err)
	}
	items := out.Data.Categories.Data
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

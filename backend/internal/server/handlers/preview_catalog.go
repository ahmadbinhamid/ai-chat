package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"sort"
	"sync"
	"time"

	"ai-chat/internal/modules/themebuild"
	"ai-chat/internal/safego"
	"ai-chat/internal/storefrontquery"
	"ai-chat/internal/themefs"
)

// filterCategoryLimit matches DataService::categoryOptions' default limit on the live storefront.
const filterCategoryLimit = 50

// previewProductsPerPage is the preview's listing page size. Live's DataService::PER_PAGE (and §7) is 15, so preview
// pages hold different products from live's from page 2 on.
const previewProductsPerPage = 12

var errCategoryNotFound = errors.New("category not found")

// buildPageContext is buildPreviewContext for one storefront URL: the page's real catalogue data filtered as the live
// storefront would filter it. Each lookup keeps its fixture on any error, so a FlowPOS outage never blanks the preview.
func buildPageContext(ctx context.Context, builder *themebuild.Service, storeAuth themefs.RequestAuth, fetchTimeout time.Duration, page storefrontquery.Request) map[string]any {
	fixture := themefs.FixtureContext()
	overlayStoreAndMenu(ctx, builder, storeAuth, fixture)
	fixture["filters"] = page.Filters.Echo(previewProductsPerPage)

	var (
		wg              sync.WaitGroup
		categories      []themefs.Category
		categoriesErr   error
		categoriesReady = make(chan struct{})
	)
	run := func(label string, fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer safego.Recover(label)
			fn()
		}()
	}

	run("preview.categories", func() {
		defer close(categoriesReady)
		fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
		defer cancel()
		categories, categoriesErr = builder.FetchPreviewCategories(fetchCtx, storeAuth, filterCategoryLimit)
		if categoriesErr == nil {
			sort.SliceStable(categories, func(i, j int) bool { return categories[i].Name < categories[j].Name })
		}
	})

	var priceMin, priceMax float64
	var priceErr error
	run("preview.priceRange", func() {
		fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
		defer cancel()
		priceMin, priceMax, priceErr = builder.FetchPreviewPriceRange(fetchCtx, storeAuth)
	})

	var listing themefs.ProductsPage
	var listingErr error
	var category *themefs.Category
	var detail themefs.ProductDetail
	var detailErr error
	if page.Kind == storefrontquery.PageProduct {
		run("preview.productDetail", func() {
			detail, detailErr = fetchProductDetail(ctx, builder, storeAuth, fetchTimeout, page.Slug)
		})
	} else {
		run("preview.products", func() {
			q := themefs.ProductsQuery{
				Limit: previewProductsPerPage, Page: page.Filters.Page, Search: page.Filters.Search,
				SortBy: page.Filters.SortBy(), MinPrice: page.Filters.MinPrice, MaxPrice: page.Filters.MaxPrice,
			}
			if page.Filters.Category != "" {
				// The tenant API filters by category id, so the slug resolves against the category list first.
				<-categoriesReady
				if categoriesErr != nil {
					listingErr = categoriesErr
					return
				}
				category = findCategory(categories, page.Filters.Category)
				if category == nil {
					listingErr = errCategoryNotFound
					return
				}
				q.CategoryID = category.ID
			}
			// Own timeout, started after the category lookup, so that lookup can't starve this fetch.
			fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
			defer cancel()
			listing, listingErr = builder.FetchPreviewProductsPage(fetchCtx, storeAuth, q)
			if listingErr != nil || len(listing.Items) == 0 {
				return
			}
			detail, detailErr = fetchProductDetail(ctx, builder, storeAuth, fetchTimeout, listing.Items[0].Slug)
		})
	}
	wg.Wait()

	if categoriesErr == nil {
		fixture["categories"] = previewCategoryList(categories)
		options := make([]any, len(categories))
		for i, c := range categories {
			options[i] = previewCategory(c)
		}
		fixture["filter_categories"] = options
	}
	logSource("categories", categoriesErr, "count", len(categories))

	if priceErr == nil {
		fixture["filter_price_range"] = map[string]any{"min": priceMin, "max": priceMax}
	}
	logSource("price range", priceErr)

	if page.Kind != storefrontquery.PageProduct {
		applyListing(fixture, page, listing, listingErr, category)
	}

	if detailErr == nil && detail.ID != 0 {
		fixture["product"] = previewProductDetail(detail)
	}
	logSource("product detail", detailErr, "slug", detail.Slug)

	return fixture
}

// applyListing sets products (and the category on a category page). An unknown category on /category/{slug} keeps the
// fixture, where live would 404; as a listing filter it is a real, empty result, as on live.
func applyListing(fixture map[string]any, page storefrontquery.Request, listing themefs.ProductsPage, err error, category *themefs.Category) {
	if errors.Is(err, errCategoryNotFound) && page.Kind == storefrontquery.PageListing {
		fixture["products"] = map[string]any{"items": []any{}, "pagination": previewPagination(themefs.ProductsPage{CurrentPage: page.Filters.Page}, previewProductsPerPage)}
		logSource("products", nil, "count", 0, "reason", "unknown category filter")
		return
	}
	// An empty, unnarrowed catalogue keeps the fixture so a merchant with no products still sees a populated page.
	if err == nil && len(listing.Items) == 0 && !narrowed(page.Filters) {
		err = errors.New("catalogue is empty")
	}
	logSource("products", err, "count", len(listing.Items), "page", page.Filters.Page)
	if err != nil {
		return
	}
	fixture["products"] = map[string]any{"items": previewProductItems(listing.Items), "pagination": previewPagination(listing, previewProductsPerPage)}
	if page.Kind == storefrontquery.PageCategory && category != nil {
		fixture["category"] = previewCategory(*category)
	}
}

func narrowed(f storefrontquery.Filters) bool {
	return f.Search != "" || f.Category != "" || f.MinPrice != nil || f.MaxPrice != nil || f.Page > 1
}

func fetchProductDetail(ctx context.Context, builder *themebuild.Service, storeAuth themefs.RequestAuth, timeout time.Duration, slug string) (themefs.ProductDetail, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return builder.FetchPreviewProductDetail(fetchCtx, storeAuth, slug)
}

func findCategory(categories []themefs.Category, slug string) *themefs.Category {
	for i := range categories {
		if categories[i].Slug == slug {
			return &categories[i]
		}
	}
	return nil
}

func logSource(what string, err error, attrs ...any) {
	if err != nil {
		slog.Info("preview: "+what+" source", "source", "fixture", "error", err)
		return
	}
	slog.Info("preview: "+what+" source", append([]any{"source", "real"}, attrs...)...)
}

// previewCategory mirrors DataService::mapCategory.
func previewCategory(c themefs.Category) map[string]any {
	imageURL := ""
	if c.Thumbnail != nil {
		imageURL = c.Thumbnail.URL
	}
	return map[string]any{
		"id": c.ID, "name": c.Name, "slug": c.Slug, "url": "/category/" + url.PathEscape(c.Slug),
		"description": derefString(c.Description), "image_url": imageURL,
	}
}

// previewCategoryList is the first page of §7's categories listing, out of the categories fetched.
func previewCategoryList(categories []themefs.Category) map[string]any {
	n := min(len(categories), themefs.StorefrontPerPage)
	items := make([]any, n)
	for i := range n {
		items[i] = previewCategory(categories[i])
	}
	lastPage := max(1, (len(categories)+themefs.StorefrontPerPage-1)/themefs.StorefrontPerPage)
	return map[string]any{"items": items, "pagination": previewPagination(themefs.ProductsPage{
		CurrentPage: 1, LastPage: lastPage, Total: len(categories),
	}, themefs.StorefrontPerPage)}
}

// previewPagination mirrors DataService::mapPagination.
func previewPagination(p themefs.ProductsPage, perPage int) map[string]any {
	current := max(1, p.CurrentPage)
	last := max(1, p.LastPage)
	hasNext := current < last
	var next, prev any
	if hasNext {
		next = current + 1
	}
	if current > 1 {
		prev = current - 1
	}
	return map[string]any{
		"page": current, "last_page": last, "total": p.Total, "per_page": perPage,
		"has_next": hasNext, "has_prev": current > 1, "next_page": next, "prev_page": prev,
	}
}

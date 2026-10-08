// Package storefrontquery validates the page path and listing filters a preview render asks for, mirroring the live
// storefront's StorefrontFilters/DataService rules so preview and live read the same query string the same way.
package storefrontquery

import (
	"math"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

type PageKind int

const (
	PageListing PageKind = iota
	PageCategory
	PageProduct
)

const (
	DefaultSort    = "name_asc"
	maxPathLen     = 512
	maxSlugLen     = 191
	maxSearchLen   = 100
	maxPriceLen    = 20
	maxPage        = 10000
	categoryPrefix = "/category/"
	productPrefix  = "/product/"
)

// sortByTenantAPI maps §7's sort values onto flowpos-backend's GET /products sort_by; anything else is ignored.
var sortByTenantAPI = map[string]string{
	"name_asc":   "name-asc",
	"name_desc":  "name-desc",
	"price_asc":  "price-asc",
	"price_desc": "price-desc",
}

var paramNames = []string{"path", "search", "sort", "category", "min_price", "max_price", "page"}

type Filters struct {
	Search   string
	Sort     string
	Category string
	MinPrice *float64
	MaxPrice *float64
	Page     int
}

type Request struct {
	// HasParams is false when the caller sent none of the recognised params, so it gets the pre-filter context unchanged.
	HasParams bool
	Kind      PageKind
	Slug      string
	Filters   Filters
}

// Parse never fails: an invalid value is dropped and the default used in its place.
func Parse(q url.Values) Request {
	req := Request{Filters: Filters{Sort: DefaultSort, Page: 1}}
	for _, name := range paramNames {
		if _, ok := q[name]; ok {
			req.HasParams = true
		}
	}

	req.Kind, req.Slug = parsePath(q.Get("path"))
	f := &req.Filters
	if s := strings.TrimSpace(q.Get("search")); utf8.RuneCountInString(s) <= maxSearchLen {
		f.Search = s
	}
	if s := q.Get("sort"); sortByTenantAPI[s] != "" {
		f.Sort = s
	}
	if s := strings.TrimSpace(q.Get("category")); validSlug(s) {
		f.Category = s
	}
	if n, err := strconv.Atoi(q.Get("page")); err == nil && n >= 1 && n <= maxPage {
		f.Page = n
	}
	f.MinPrice, f.MaxPrice = parsePrice(q.Get("min_price")), parsePrice(q.Get("max_price"))
	if f.MinPrice != nil && f.MaxPrice != nil && *f.MinPrice > *f.MaxPrice {
		f.MinPrice, f.MaxPrice = f.MaxPrice, f.MinPrice
	}
	// Live forces the category filter to the page's own category on /category/{slug}.
	if req.Kind == PageCategory {
		f.Category = req.Slug
	}
	return req
}

func parsePath(raw string) (PageKind, string) {
	if raw == "" || len(raw) > maxPathLen || !strings.HasPrefix(raw, "/") || strings.ContainsAny(raw, "?#") {
		return PageListing, ""
	}
	p := strings.TrimSuffix(raw, "/")
	for prefix, kind := range map[string]PageKind{categoryPrefix: PageCategory, productPrefix: PageProduct} {
		rest, ok := strings.CutPrefix(p, prefix)
		if !ok {
			continue
		}
		slug, err := url.PathUnescape(rest)
		if err != nil || !validSlug(slug) {
			return PageListing, ""
		}
		return kind, slug
	}
	return PageListing, ""
}

func validSlug(s string) bool {
	return s != "" && len(s) <= maxSlugLen && !strings.ContainsAny(s, "/\\") && s != "." && s != ".."
}

// parsePrice mirrors DataService::parseOptionalPrice: numeric only, clamped at zero.
func parsePrice(raw string) *float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxPriceLen {
		return nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	v = math.Max(0, v)
	return &v
}

// SortBy is the tenant API's sort_by for this filter set's sort.
func (f Filters) SortBy() string {
	return sortByTenantAPI[f.Sort]
}

// Echo is §7's filters object, shaped like DataService::products' echo.
func (f Filters) Echo(perPage int) map[string]any {
	return map[string]any{
		"search": f.Search, "sort": f.Sort, "category": f.Category,
		"min_price": formatPrice(f.MinPrice), "max_price": formatPrice(f.MaxPrice), "per_page": perPage,
	}
}

// formatPrice mirrors DataService::formatPriceFilter: two decimals with trailing zeros trimmed.
func formatPrice(p *float64) string {
	if p == nil {
		return ""
	}
	s := strings.TrimRight(strings.TrimRight(strconv.FormatFloat(*p, 'f', 2, 64), "0"), ".")
	if s == "" {
		return "0"
	}
	return s
}

package storefrontquery

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func ptr(v float64) *float64 { return &v }

func TestParse(t *testing.T) {
	defaults := Filters{Sort: DefaultSort, Page: 1}
	tests := []struct {
		name  string
		query string
		want  Request
	}{
		{"no params", "", Request{Filters: defaults}},
		{"unrelated param only", "foo=bar", Request{Filters: defaults}},
		{"products path", "path=/products", Request{HasParams: true, Filters: defaults}},
		{"category path", "path=/category/shoes", Request{HasParams: true, Kind: PageCategory, Slug: "shoes",
			Filters: Filters{Sort: DefaultSort, Category: "shoes", Page: 1}}},
		{"category path overrides category param", "path=/category/shoes&category=hats", Request{HasParams: true,
			Kind: PageCategory, Slug: "shoes", Filters: Filters{Sort: DefaultSort, Category: "shoes", Page: 1}}},
		{"category path trailing slash", "path=/category/shoes/", Request{HasParams: true, Kind: PageCategory, Slug: "shoes",
			Filters: Filters{Sort: DefaultSort, Category: "shoes", Page: 1}}},
		{"escaped slug", "path=/product/blue%2520mug", Request{HasParams: true, Kind: PageProduct, Slug: "blue mug", Filters: defaults}},
		{"product path", "path=/product/blue-mug", Request{HasParams: true, Kind: PageProduct, Slug: "blue-mug", Filters: defaults}},
		{"nested slug ignored", "path=/product/a/b", Request{HasParams: true, Filters: defaults}},
		{"dot slug ignored", "path=/category/..", Request{HasParams: true, Filters: defaults}},
		{"relative path ignored", "path=products", Request{HasParams: true, Filters: defaults}},
		{"path with query ignored", "path=/category/shoes%3Fx%3D1", Request{HasParams: true, Filters: defaults}},
		{"all filters", "search=+mug+&sort=price_desc&category=shoes&min_price=5&max_price=20.5&page=2", Request{HasParams: true,
			Filters: Filters{Search: "mug", Sort: "price_desc", Category: "shoes", MinPrice: ptr(5), MaxPrice: ptr(20.5), Page: 2}}},
		{"invalid sort", "sort=random", Request{HasParams: true, Filters: defaults}},
		{"tenant api sort spelling rejected", "sort=price-asc", Request{HasParams: true, Filters: defaults}},
		{"non-numeric price", "min_price=abc&max_price=1e400", Request{HasParams: true, Filters: defaults}},
		{"NaN price", "min_price=NaN", Request{HasParams: true, Filters: defaults}},
		{"negative price clamps to zero", "min_price=-5", Request{HasParams: true, Filters: Filters{Sort: DefaultSort, MinPrice: ptr(0), Page: 1}}},
		{"swapped prices", "min_price=30&max_price=10", Request{HasParams: true,
			Filters: Filters{Sort: DefaultSort, MinPrice: ptr(10), MaxPrice: ptr(30), Page: 1}}},
		{"negative page", "page=-1", Request{HasParams: true, Filters: defaults}},
		{"zero page", "page=0", Request{HasParams: true, Filters: defaults}},
		{"fractional page", "page=1.5", Request{HasParams: true, Filters: defaults}},
		{"huge page", "page=99999999", Request{HasParams: true, Filters: defaults}},
		{"overlong search", "search=" + strings.Repeat("a", maxSearchLen+1), Request{HasParams: true, Filters: defaults}},
		{"overlong category", "category=" + strings.Repeat("a", maxSlugLen+1), Request{HasParams: true, Filters: defaults}},
		{"overlong path", "path=/category/" + strings.Repeat("a", maxPathLen), Request{HasParams: true, Filters: defaults}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatal(err)
			}
			if got := Parse(q); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Parse(%q) = %+v, want %+v", tt.query, got, tt.want)
			}
		})
	}
}

func TestFilters_SortBy(t *testing.T) {
	for sort, want := range map[string]string{
		"name_asc": "name-asc", "name_desc": "name-desc", "price_asc": "price-asc", "price_desc": "price-desc", "bogus": "",
	} {
		if got := (Filters{Sort: sort}).SortBy(); got != want {
			t.Errorf("SortBy(%q) = %q, want %q", sort, got, want)
		}
	}
}

func TestFilters_Echo(t *testing.T) {
	tests := []struct {
		name string
		in   Filters
		want map[string]any
	}{
		{"empty", Filters{Sort: DefaultSort, Page: 1}, map[string]any{
			"search": "", "sort": "name_asc", "category": "", "min_price": "", "max_price": "", "per_page": 15}},
		{"trims trailing zeros", Filters{Sort: "price_asc", Category: "shoes", MinPrice: ptr(5), MaxPrice: ptr(20.5), Page: 3}, map[string]any{
			"search": "", "sort": "price_asc", "category": "shoes", "min_price": "5", "max_price": "20.5", "per_page": 15}},
		{"zero price", Filters{Sort: DefaultSort, MinPrice: ptr(0)}, map[string]any{
			"search": "", "sort": "name_asc", "category": "", "min_price": "0", "max_price": "", "per_page": 15}},
		{"rounds to two decimals", Filters{Sort: DefaultSort, MaxPrice: ptr(9.999)}, map[string]any{
			"search": "", "sort": "name_asc", "category": "", "min_price": "", "max_price": "10", "per_page": 15}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.Echo(15); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Echo() = %v, want %v", got, tt.want)
			}
		})
	}
}

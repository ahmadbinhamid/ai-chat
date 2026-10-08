package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"ai-chat/internal/auth"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/modules/themebuild"
	"ai-chat/internal/themefs"
)

// catalogFlowpos fakes the tenant API's catalogue endpoints, recording every /products query; paths in fail 500.
type catalogFlowpos struct {
	mu       sync.Mutex
	listings []url.Values
	paths    []string
	fail     map[string]bool
	// noCategories serves an empty category list, like a store that has published none.
	noCategories bool
}

func (f *catalogFlowpos) serve(t *testing.T) *httptest.Server {
	t.Helper()
	products := []map[string]any{sampleRealProduct(1, "Red Shoe", "red-shoe"), sampleRealProduct(2, "Blue Shoe", "blue-shoe")}
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		failed := f.fail[r.URL.Path]
		q := r.URL.Query()
		if r.URL.Path == "/products" && q.Get("limit") != "1" {
			f.listings = append(f.listings, q)
		}
		f.mu.Unlock()
		if failed {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		switch r.URL.Path {
		case "/store":
			write(w, map[string]any{"data": map[string]any{"store": map[string]any{"name": "Fleure"}}})
		case "/categories":
			if f.noCategories {
				write(w, map[string]any{"data": map[string]any{"categories": map[string]any{"data": []any{}}}})
				return
			}
			write(w, map[string]any{"data": map[string]any{"categories": map[string]any{"data": []map[string]any{
				{"id": 9, "name": "Shoes", "slug": "shoes", "description": "All the shoes", "thumbnail": map[string]any{"url": "https://cdn.example.com/shoes.jpg"}},
				{"id": 4, "name": "Hats", "slug": "hats"},
			}}}})
		case "/products":
			if q.Get("limit") == "1" {
				price := 3.0
				if q.Get("sort_by") == "price-desc" {
					price = 80
				}
				write(w, map[string]any{"data": map[string]any{"products": map[string]any{
					"current_page": 1, "last_page": 1, "per_page": 1, "total": 1,
					"data": []map[string]any{{"id": 5, "name": "P", "slug": "p", "price": price}},
				}}})
				return
			}
			write(w, map[string]any{"data": map[string]any{"products": map[string]any{
				"current_page": 2, "last_page": 3, "per_page": 12, "total": 32, "data": products,
			}}})
		case "/products/blue-mug":
			_, _ = w.Write([]byte(`{"data":{"product":{"id":77,"slug":"blue-mug","name":"Blue Mug","price":9}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func (f *catalogFlowpos) called(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.paths {
		if p == path {
			return true
		}
	}
	return false
}

func (f *catalogFlowpos) lastListing(t *testing.T) url.Values {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.listings) == 0 {
		t.Fatal("expected a GET /products listing call, got none")
	}
	return f.listings[len(f.listings)-1]
}

func getPreviewContext(t *testing.T, flowposURL, rawQuery string) map[string]any {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("auth_identity", auth.Identity{UserID: 1, TenantID: 1})
		c.Set("auth_token", "t")
	})
	svc := themebuild.NewService(nil, chat.NewService(nil), nil, themefs.NewStore(flowposURL), nil)
	r.GET("/preview/context", NewPreviewHandler(svc).Context)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/preview/context?"+rawQuery, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /preview/context?%s: %d %s", rawQuery, w.Code, w.Body)
	}
	var out struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	assertPreviewContextKeySet(t, out.Data)
	return out.Data
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

func itemNames(products any) []any {
	var names []any
	items, _ := asMap(products)["items"].([]any)
	for _, it := range items {
		names = append(names, asMap(it)["name"])
	}
	return names
}

func TestPreviewContext_NoParamsKeepsTodaysBehaviour(t *testing.T) {
	fake := &catalogFlowpos{}
	ts := fake.serve(t)
	got := getPreviewContext(t, ts.URL, "")

	q := fake.lastListing(t)
	if q.Get("limit") != fmt.Sprint(previewProductLimit) || q.Has("sort_by") || q.Has("page") || q.Has("categories") {
		t.Errorf("want today's unfiltered %d-product call, got %v", previewProductLimit, q)
	}
	if fake.called("/categories") {
		t.Error("no-params request must not fetch categories")
	}
	pagination := asMap(asMap(got["products"])["pagination"])
	if pagination["page"] != 1.0 || pagination["last_page"] != 1.0 || pagination["has_next"] != false {
		t.Errorf("want today's single-page pagination, got %v", pagination)
	}
	if asMap(got["category"])["name"] != "Sample Category" || asMap(got["filters"])["sort"] != "" {
		t.Errorf("want fixture category and filters, got %v / %v", got["category"], got["filters"])
	}
}

func TestPreviewContext_CategoryFilter(t *testing.T) {
	fake := &catalogFlowpos{}
	ts := fake.serve(t)
	got := getPreviewContext(t, ts.URL, "path=/products&category=shoes")

	q := fake.lastListing(t)
	if q.Get("categories") != "9" || q.Get("limit") != "12" || q.Get("page") != "1" || q.Get("sort_by") != "name-asc" {
		t.Errorf("want /products filtered to category id 9, page 1 of 12, sorted by name; got %v", q)
	}
	if asMap(got["filters"])["category"] != "shoes" {
		t.Errorf("want filters.category echoed, got %v", got["filters"])
	}
	options, _ := got["filter_categories"].([]any)
	if len(options) != 2 || asMap(options[0])["slug"] != "hats" || asMap(options[1])["url"] != "/category/shoes" {
		t.Errorf("want real categories sorted by name, got %v", options)
	}
	if r := asMap(got["filter_price_range"]); r["min"] != 3.0 || r["max"] != 80.0 {
		t.Errorf("want real price bounds 3..80, got %v", r)
	}
}

func TestPreviewContext_StoreWithNoCategoriesShowsNone(t *testing.T) {
	fake := &catalogFlowpos{noCategories: true}
	ts := fake.serve(t)
	got := getPreviewContext(t, ts.URL, "path=/products")

	if options, ok := got["filter_categories"].([]any); !ok || len(options) != 0 {
		t.Errorf("want no filter categories, not the samples, got %v", got["filter_categories"])
	}
	if items, ok := asMap(got["categories"])["items"].([]any); !ok || len(items) != 0 {
		t.Errorf("want no categories, not the samples, got %v", got["categories"])
	}
}

func TestPreviewContext_UnknownCategoryFilterIsAnEmptyListing(t *testing.T) {
	fake := &catalogFlowpos{}
	ts := fake.serve(t)
	got := getPreviewContext(t, ts.URL, "path=/products&category=boots")

	if names := itemNames(got["products"]); len(names) != 0 {
		t.Errorf("want no products for a category the store doesn't have, got %v", names)
	}
}

func TestPreviewContext_CategoryPage(t *testing.T) {
	fake := &catalogFlowpos{}
	ts := fake.serve(t)
	got := getPreviewContext(t, ts.URL, "path=/category/shoes&sort=price_desc&page=2")

	q := fake.lastListing(t)
	if q.Get("categories") != "9" || q.Get("sort_by") != "price-desc" || q.Get("page") != "2" {
		t.Errorf("want the category's products sorted and paged, got %v", q)
	}
	category := asMap(got["category"])
	if category["name"] != "Shoes" || category["description"] != "All the shoes" || category["image_url"] != "https://cdn.example.com/shoes.jpg" {
		t.Errorf("want the real category, got %v", category)
	}
	if names := itemNames(got["products"]); len(names) != 2 || names[0] != "Red Shoe" {
		t.Errorf("want the category's real products, got %v", names)
	}
	pagination := asMap(asMap(got["products"])["pagination"])
	if pagination["page"] != 2.0 || pagination["last_page"] != 3.0 || pagination["total"] != 32.0 ||
		pagination["next_page"] != 3.0 || pagination["prev_page"] != 1.0 || pagination["has_next"] != true ||
		pagination["per_page"] != 12.0 {
		t.Errorf("want the real pagination, got %v", pagination)
	}
	if f := asMap(got["filters"]); f["category"] != "shoes" || f["sort"] != "price_desc" || f["per_page"] != 12.0 {
		t.Errorf("want the category and sort echoed, got %v", f)
	}
}

func TestPreviewContext_ProductPage(t *testing.T) {
	fake := &catalogFlowpos{}
	ts := fake.serve(t)
	got := getPreviewContext(t, ts.URL, "path=/product/blue-mug")

	if asMap(got["product"])["name"] != "Blue Mug" {
		t.Errorf("want the real product for the path's slug, got %v", asMap(got["product"])["name"])
	}
}

func TestPreviewContext_InvalidFiltersAreIgnored(t *testing.T) {
	for _, query := range []string{"sort=cheapest", "min_price=abc&max_price=lots", "page=-1"} {
		t.Run(query, func(t *testing.T) {
			fake := &catalogFlowpos{}
			ts := fake.serve(t)
			got := getPreviewContext(t, ts.URL, "path=/products&"+query)

			q := fake.lastListing(t)
			if q.Get("sort_by") != "name-asc" || q.Has("min_price") || q.Has("max_price") || q.Get("page") != "1" {
				t.Errorf("want the invalid value dropped from the /products call, got %v", q)
			}
			f := asMap(got["filters"])
			if f["sort"] != "name_asc" || f["min_price"] != "" || f["max_price"] != "" {
				t.Errorf("want default filters echoed, got %v", f)
			}
			if names := itemNames(got["products"]); len(names) != 2 {
				t.Errorf("want the real listing still served, got %v", names)
			}
		})
	}
}

func TestPreviewContext_LookupErrorsFallBackToFixture(t *testing.T) {
	fixture := themefs.FixtureContext()
	tests := []struct {
		name  string
		fail  string
		query string
		keys  []string
	}{
		{"categories", "/categories", "path=/products", []string{"categories", "filter_categories"}},
		{"categories needed by a category filter", "/categories", "path=/products&category=shoes", []string{"products", "categories"}},
		{"category page", "/categories", "path=/category/shoes", []string{"category", "products"}},
		{"products and price range", "/products", "path=/products", []string{"products", "filter_price_range", "product"}},
		{"product detail", "/products/blue-mug", "path=/product/blue-mug", []string{"product"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &catalogFlowpos{fail: map[string]bool{tt.fail: true}}
			ts := fake.serve(t)
			got := getPreviewContext(t, ts.URL, tt.query)
			for _, key := range tt.keys {
				want, _ := json.Marshal(fixture[key])
				have, _ := json.Marshal(got[key])
				if string(want) != string(have) {
					t.Errorf("%s: want the fixture after %s failed, got %s", key, tt.fail, have)
				}
			}
		})
	}
}

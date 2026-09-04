package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"ai-chat/internal/modules/chat"
	"ai-chat/internal/modules/themebuild"
	"ai-chat/internal/themefs"
)

// fakeFlowposProductsServer serves GET /products with a fixed JSON payload
// (and /store, if storeName is non-empty, so a test can confirm the
// existing store overlay still works unaffected alongside the new products
// one) — every other path 404s, exactly like the real fetch failing.
func fakeFlowposProductsServer(t *testing.T, storeName string, productsBody []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/products" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(productsBody)
			return
		}
		if r.URL.Path == "/store" && storeName != "" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"store": map[string]any{"name": storeName}}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

// currentPage is always 1 and perPage always matches the preview's own cap
// (3, FixtureProducts()' own item count — see buildPreviewContext): every
// test here simulates fetching the preview's one and only page at that same
// size, so neither is a parameter — see
// TestBuildPreviewContext_PaginationNeverAdvertisesANextPage for the test
// that specifically covers a real catalogue with more pages than that.
func rawProductsResponse(t *testing.T, lastPage, total int, products []map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"data": map[string]any{
			"products": map[string]any{
				"current_page": 1,
				"data":         products,
				"last_page":    lastPage,
				"per_page":     3,
				"total":        total,
			},
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal fake products response: %v", err)
	}
	return body
}

func sampleRealProduct(id int, name, slug string) map[string]any {
	return map[string]any{
		"id": id, "name": name, "slug": slug, "description": "A real product, fetched live.",
		"price": 12.5, "compare_price": 15.0, "has_variants": false,
		"sku": "RP-1", "barcode": "1234567890123",
		"attachments":     []map[string]any{{"url": "https://cdn.example.com/rp1.jpg"}},
		"default_variant": map[string]any{"id": 42},
	}
}

// assertPreviewContextKeySet confirms buildPreviewContext's response shape
// never changes regardless of whether products came back real or fixture —
// the frontend's LiquidJS preview consumes this key set unchanged (see
// buildPreviewContext's own doc comment).
func assertPreviewContextKeySet(t *testing.T, ctx map[string]any) {
	t.Helper()
	want := []string{
		"page", "store", "theme", "menu", "path", "customer", "auth_check", "environment",
		"csrf_token", "product", "products", "category", "categories", "filter_categories",
		"filters", "filter_price_range", "basket",
	}
	got := make([]string, 0, len(ctx))
	for k := range ctx {
		got = append(got, k)
	}
	sort.Strings(want)
	sort.Strings(got)
	if len(want) != len(got) {
		t.Fatalf("key set changed: want %v, got %v", want, got)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("key set changed: want %v, got %v", want, got)
		}
	}
}

// TestBuildPreviewContext_RealProductsFetched covers the happy path: real
// products overlay products, store overlays independently and correctly
// alongside it (proving the new fetch doesn't interfere with the existing
// ones), and everything else (product, category, ...) stays fixture.
func TestBuildPreviewContext_RealProductsFetched(t *testing.T) {
	body := rawProductsResponse(t, 1, 1, []map[string]any{sampleRealProduct(1, "Real Widget", "real-widget")})
	ts := fakeFlowposProductsServer(t, "Fleure", body)
	defer ts.Close()

	buildSvc := themebuild.NewService(nil, chat.NewService(nil), nil, themefs.NewStore(ts.URL), nil)
	ctx := buildPreviewContext(context.Background(), buildSvc, themefs.RequestAuth{Token: "t", TenantID: 1}, time.Second)
	assertPreviewContextKeySet(t, ctx)

	store, _ := ctx["store"].(map[string]any)
	if store["name"] != "Fleure" {
		t.Errorf("expected the store overlay to still work alongside the products one, got %+v", store)
	}

	products, ok := ctx["products"].(map[string]any)
	if !ok {
		t.Fatalf("expected products to be a map, got %T", ctx["products"])
	}
	items, ok := products["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("expected exactly 1 real product item, got %+v", products["items"])
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("expected a product item map, got %T", items[0])
	}
	if item["name"] != "Real Widget" || item["slug"] != "real-widget" {
		t.Errorf("expected real product data, got %+v", item)
	}
	if item["url"] != "/product/real-widget" {
		t.Errorf("expected url to use the singular /product/ resource route (matches flowpos-backend's PageResolver), got %v", item["url"])
	}
	if item["price_formatted"] != "£12.50" {
		t.Errorf("expected price_formatted to be computed from the real price, got %v", item["price_formatted"])
	}
	if item["on_sale"] != true {
		t.Errorf("expected on_sale true (compare_price 15.0 > price 12.5), got %v", item["on_sale"])
	}

	// product (the detail-page singular) must stay on the fixture — see
	// buildPreviewContext's own doc comment on why.
	product, _ := ctx["product"].(map[string]any)
	if product["name"] != "Sample Product" {
		t.Errorf("expected product to stay on the fixture, got %+v", product)
	}
	// category/categories untouched by this change at all.
	category, _ := ctx["category"].(map[string]any)
	if category["name"] != "Sample Category" {
		t.Errorf("expected category to stay on the fixture, got %+v", category)
	}
}

// TestBuildPreviewContext_ProductsFetchErrorKeepsFixture covers the first
// fail-open case: a hard fetch failure (every path 404s here) must never
// surface as an error to the caller — buildPreviewContext has no error
// return at all, so this is really "keeps rendering with fixture data."
func TestBuildPreviewContext_ProductsFetchErrorKeepsFixture(t *testing.T) {
	ts := fakeFlowposProductsServer(t, "", nil) // /products 404s
	defer ts.Close()

	buildSvc := themebuild.NewService(nil, chat.NewService(nil), nil, themefs.NewStore(ts.URL), nil)
	ctx := buildPreviewContext(context.Background(), buildSvc, themefs.RequestAuth{Token: "t", TenantID: 1}, time.Second)
	assertPreviewContextKeySet(t, ctx)

	products, _ := ctx["products"].(map[string]any)
	items, _ := products["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("expected the 3-item fixture product list on fetch error, got %d items: %+v", len(items), items)
	}
	first, _ := items[0].(map[string]any)
	if first["name"] != "Sample Product" {
		t.Errorf("expected fixture product data on fetch error, got %+v", first)
	}
}

// TestBuildPreviewContext_ZeroProductsKeepsFixture covers the second,
// more important fail-open case: the fetch SUCCEEDS but the tenant's real
// catalogue is empty. A merchant with no products yet must still see a
// populated-looking shop page, not a blank one they'd mistake for a broken
// theme (see buildPreviewContext's own doc comment).
func TestBuildPreviewContext_ZeroProductsKeepsFixture(t *testing.T) {
	body := rawProductsResponse(t, 1, 0, []map[string]any{})
	ts := fakeFlowposProductsServer(t, "", body)
	defer ts.Close()

	buildSvc := themebuild.NewService(nil, chat.NewService(nil), nil, themefs.NewStore(ts.URL), nil)
	ctx := buildPreviewContext(context.Background(), buildSvc, themefs.RequestAuth{Token: "t", TenantID: 1}, time.Second)
	assertPreviewContextKeySet(t, ctx)

	products, _ := ctx["products"].(map[string]any)
	items, _ := products["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("expected the 3-item fixture product list when the real catalogue is empty, got %d items: %+v", len(items), items)
	}
}

// TestBuildPreviewContext_CapsProductsAtFixtureLength confirms the cap is
// enforced even if flowpos-backend ignores the "limit" query param and
// returns more items than asked for — a merchant with a huge catalogue
// must not blow up this response (see themefs.Store.FetchProducts' own
// doc comment on the defensive re-cap).
func TestBuildPreviewContext_CapsProductsAtFixtureLength(t *testing.T) {
	var raw []map[string]any
	for i := 1; i <= 5; i++ {
		raw = append(raw, sampleRealProduct(i, "Product", "product-"+string(rune('0'+i))))
	}
	// Server misbehaves: reports 5 items on a page it claims is size 3.
	body := rawProductsResponse(t, 1, 5, raw)
	ts := fakeFlowposProductsServer(t, "", body)
	defer ts.Close()

	buildSvc := themebuild.NewService(nil, chat.NewService(nil), nil, themefs.NewStore(ts.URL), nil)
	ctx := buildPreviewContext(context.Background(), buildSvc, themefs.RequestAuth{Token: "t", TenantID: 1}, time.Second)
	assertPreviewContextKeySet(t, ctx)

	products, _ := ctx["products"].(map[string]any)
	items, _ := products["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("expected the cap (3, FixtureProducts()' own item count) enforced even though the fake server returned 5, got %d", len(items))
	}
}

// TestBuildPreviewContext_PaginationNeverAdvertisesANextPage locks in the
// actual fix for a real reported bug: flowpos-backend's paginator response
// here claims a real second page exists (last_page: 3), but the preview
// only ever fetches one page and has no mechanism to fetch or render
// another — reporting has_next: true drew a live-looking "Next" button that
// did nothing when a merchant clicked it. Pagination must always claim
// exactly one page, regardless of what the real catalogue's page count is.
func TestBuildPreviewContext_PaginationNeverAdvertisesANextPage(t *testing.T) {
	body := rawProductsResponse(t, 3, 9, []map[string]any{
		sampleRealProduct(1, "Product One", "product-one"),
		sampleRealProduct(2, "Product Two", "product-two"),
		sampleRealProduct(3, "Product Three", "product-three"),
	})
	ts := fakeFlowposProductsServer(t, "", body)
	defer ts.Close()

	buildSvc := themebuild.NewService(nil, chat.NewService(nil), nil, themefs.NewStore(ts.URL), nil)
	ctx := buildPreviewContext(context.Background(), buildSvc, themefs.RequestAuth{Token: "t", TenantID: 1}, time.Second)

	products, _ := ctx["products"].(map[string]any)
	pagination, ok := products["pagination"].(map[string]any)
	if !ok {
		t.Fatalf("expected pagination to be a map, got %T", products["pagination"])
	}
	if pagination["has_next"] != false || pagination["has_prev"] != false {
		t.Errorf("expected has_next/has_prev both false regardless of the real catalogue's page count, got %+v", pagination)
	}
	if pagination["page"] != 1 || pagination["last_page"] != 1 {
		t.Errorf("expected page 1 of 1 (matching that only one page is ever fetched), got page=%v last_page=%v", pagination["page"], pagination["last_page"])
	}
	if pagination["next_page"] != nil || pagination["prev_page"] != nil {
		t.Errorf("expected next_page/prev_page both nil, got next=%v prev=%v", pagination["next_page"], pagination["prev_page"])
	}
}

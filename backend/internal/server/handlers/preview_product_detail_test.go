package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"ai-chat/internal/modules/chat"
	"ai-chat/internal/modules/themebuild"
	"ai-chat/internal/themecheck"
	"ai-chat/internal/themefs"
)

// showResponse mirrors flowpos-backend's GET /products/{slug}: Laravel snake_cases relations, the default variant sits outside
// variants, and each variant's add_on_groups arrive without their add-ons.
const showResponse = `{"data":{"product":{
  "id": 7, "slug": "numbing-cream", "name": "Numbing Cream", "description": "<p>Fast acting.</p>",
  "price": 30.0, "compare_price": 35.0, "sku": "NC-1", "barcode": null, "variants_count": 2,
  "attachments": [{"url": "https://cdn.example.com/nc-1.jpg"}, {"url": "https://cdn.example.com/nc-2.jpg"}],
  "default_variant": {"id": 70, "sku": null, "barcode": "5000000000001", "price": 30.0, "is_available": true,
    "items": [], "attachments": [], "add_on_groups": [{"id": 3, "name": "Extras", "is_active": true, "min_selection": 0, "max_selection": 2}]},
  "variants": [
    {"id": 71, "sku": "NC-30", "barcode": "5000000000002", "price": 24.5, "is_available": true,
     "items": [{"id": 11, "ctype_id": 5, "name": "30g", "choice_type": {"id": 5, "label": "Size"}}],
     "attachments": [{"url": "https://cdn.example.com/nc-30.jpg"}],
     "add_on_groups": [{"id": 3, "name": "Extras", "is_active": true, "min_selection": 0, "max_selection": 2},
                       {"id": 4, "name": "Broken group", "is_active": true, "min_selection": 1, "max_selection": 1}]},
    {"id": 72, "sku": "NC-60", "barcode": null, "price": 39.0, "is_available": false,
     "items": [{"id": 12, "ctype_id": 5, "name": "60g", "choice_type": {"id": 5, "label": "Size"}}],
     "attachments": [], "add_on_groups": []}
  ]
}}}`

const addOnGroup3Response = `{"data":{"addOnGroup":{"id": 3, "name": "Extras", "is_active": true, "min_selection": 0, "max_selection": 2,
  "addons": [
    {"id": 31, "name": "Gift wrap", "price": 2.5, "max_quantity": 1, "is_active": true, "sort_order": 2},
    {"id": 32, "name": "Applicator", "price": 1.0, "max_quantity": 3, "is_active": true, "sort_order": 1},
    {"id": 33, "name": "Retired", "price": 9.0, "max_quantity": 1, "is_active": false, "sort_order": 0}
  ]}}}`

// fakeFlowposDetailServer serves the products list, the detail for "numbing-cream" and add-on group 3; group 4 500s, anything else 404s.
func fakeFlowposDetailServer(t *testing.T, detailStatus int) *httptest.Server {
	t.Helper()
	list := rawProductsResponse(t, 1, 1, []map[string]any{sampleRealProduct(7, "Numbing Cream", "numbing-cream")})
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/products":
			_, _ = w.Write(list)
		case "/products/numbing-cream":
			w.WriteHeader(detailStatus)
			if detailStatus == http.StatusOK {
				_, _ = w.Write([]byte(showResponse))
			}
		case "/addon-groups/3":
			_, _ = w.Write([]byte(addOnGroup3Response))
		case "/addon-groups/4":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func previewProductFromFake(t *testing.T, detailStatus int) map[string]any {
	t.Helper()
	ts := fakeFlowposDetailServer(t, detailStatus)
	t.Cleanup(ts.Close)
	buildSvc := themebuild.NewService(nil, chat.NewService(nil), nil, themefs.NewStore(ts.URL), nil)
	ctx := buildPreviewContext(context.Background(), buildSvc, themefs.RequestAuth{Token: "t", TenantID: 1}, time.Second)
	assertPreviewContextKeySet(t, ctx)
	product, ok := ctx["product"].(map[string]any)
	if !ok {
		t.Fatalf("expected product to be a map, got %T", ctx["product"])
	}
	return product
}

func TestBuildPreviewContext_RealProductDetail(t *testing.T) {
	p := previewProductFromFake(t, http.StatusOK)

	scalars := map[string]any{
		"name": "Numbing Cream", "title": "Numbing Cream", "slug": "numbing-cream", "url": "/product/numbing-cream",
		"image_url": "https://cdn.example.com/nc-1.jpg", "price_amount": 24.5, "price_formatted": "£24.50",
		"compare_at_price_formatted": "£35.00", "on_sale": 1, "barcode": "5000000000001", "default_variant_id": 70,
		"has_variants": 1, "can_quick_add": 0, "show_add_to_cart": 0, "variant_count": 2, "has_choices": 1, "has_addons": 1,
	}
	for k, want := range scalars {
		if got := p[k]; got != want {
			t.Errorf("%s = %#v, want %#v", k, got, want)
		}
	}

	variants := p["variants"].([]any)
	v1 := variants[0].(map[string]any)
	if v1["label"] != "30g" || v1["price_amount"] != 24.5 || v1["image_url"] != "https://cdn.example.com/nc-30.jpg" {
		t.Errorf("unexpected first variant %+v", v1)
	}
	if !reflect.DeepEqual(v1["options"], map[string]any{"5": 11}) {
		t.Errorf("options = %#v, want choice type 5 -> item 11", v1["options"])
	}
	if variants[1].(map[string]any)["is_available"] != 0 {
		t.Errorf("expected the 60g variant unavailable, got %+v", variants[1])
	}

	var decoded, want []any
	if err := json.Unmarshal([]byte(p["variants_json"].(string)), &decoded); err != nil {
		t.Fatalf("variants_json does not decode: %v", err)
	}
	raw, _ := json.Marshal(variants)
	_ = json.Unmarshal(raw, &want)
	if !reflect.DeepEqual(decoded, want) {
		t.Errorf("variants_json does not match variants")
	}

	choices := p["choices"].([]any)
	if len(choices) != 1 || choices[0].(map[string]any)["label"] != "Size" || len(choices[0].(map[string]any)["items"].([]any)) != 2 {
		t.Errorf("unexpected choices %+v", choices)
	}

	// Group 3 is filled from /addon-groups/3 (inactive add-on dropped, sorted by sort_order); group 4's fetch fails, so it's dropped.
	groups := p["addon_groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("expected only the fetchable group, got %+v", groups)
	}
	addons := groups[0].(map[string]any)["addons"].([]any)
	if len(addons) != 2 || addons[0].(map[string]any)["name"] != "Applicator" || addons[1].(map[string]any)["price_formatted"] != "£2.50" {
		t.Errorf("unexpected add-ons %+v", addons)
	}
	if !reflect.DeepEqual(p["addon_groups"], p["add_on_groups"]) {
		t.Error("add_on_groups must equal addon_groups")
	}
	flat := p["addons"].([]any)
	if len(flat) != 2 || flat[0].(map[string]any)["group_name"] != "Extras" || flat[0].(map[string]any)["group_id"] != 3 {
		t.Errorf("unexpected flattened add-ons %+v", flat)
	}
}

func TestBuildPreviewContext_ProductDetailFetchErrorKeepsFixture(t *testing.T) {
	p := previewProductFromFake(t, http.StatusNotFound)
	if p["name"] != "Sample Product" {
		t.Errorf("expected the fixture product on a detail fetch error, got %v", p["name"])
	}
}

func TestPreviewProductDetail_PassesKnownFields(t *testing.T) {
	p := previewProductFromFake(t, http.StatusOK)
	var b strings.Builder
	probeFields(&b, "product", p, 0)
	if !strings.Contains(b.String(), "x0.options") {
		t.Fatalf("probe did not reach variant options; got:\n%s", b.String())
	}
	// variants[].options is a map keyed by choice-type id, read by subscript in themes, never as named fields.
	probe := strings.ReplaceAll(b.String(), "x0.options.5", "x0.options['5']")
	files := []themecheck.ProposedFile{{Path: "components/product-probe.liquid", Content: probe}}
	for _, f := range themecheck.Check(themecheck.Proposal{Files: files}, themecheck.Snapshot{}) {
		if f.Rule == "known-fields" {
			t.Errorf("mapped detail field rejected by known-fields: %s", f.Message)
		}
	}
}

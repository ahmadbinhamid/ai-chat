package handlers

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"ai-chat/internal/themecheck"
	"ai-chat/internal/themefs"
)

func TestPreviewProductItem_PriceAmountIsPounds(t *testing.T) {
	item := previewProductItem(themefs.Product{ID: 1, Name: "Real Widget", Slug: "real-widget", Price: 12.5})
	if item["price_amount"] != 12.5 {
		t.Errorf("price_amount = %v, want 12.5", item["price_amount"])
	}
	if item["price_formatted"] != "£12.50" {
		t.Errorf("price_formatted = %v, want £12.50", item["price_formatted"])
	}
}

func TestPreviewProductItem_TitleMatchesName(t *testing.T) {
	item := previewProductItem(themefs.Product{ID: 1, Name: "Real Widget", Slug: "real-widget"})
	if item["title"] != item["name"] {
		t.Errorf("title %v != name %v", item["title"], item["name"])
	}
}

func TestPreviewProductItem_QuickAdd(t *testing.T) {
	defaultVariant := &themefs.ProductVariantRef{ID: 42}
	tests := []struct {
		name    string
		product themefs.Product
		want    int
	}{
		{"no variants with default variant", themefs.Product{HasVariants: false, DefaultVariant: defaultVariant}, 1},
		{"has variants", themefs.Product{HasVariants: true, DefaultVariant: defaultVariant}, 0},
		{"no default variant", themefs.Product{HasVariants: false}, 0},
		{"has variants and no default variant", themefs.Product{HasVariants: true}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := previewProductItem(tt.product)
			if item["can_quick_add"] != tt.want || item["show_add_to_cart"] != tt.want {
				t.Errorf("can_quick_add = %v, show_add_to_cart = %v, want both %d", item["can_quick_add"], item["show_add_to_cart"], tt.want)
			}
		})
	}
}

func TestPreviewProductsPagination_PerPageAlways15(t *testing.T) {
	for _, n := range []int{0, 1, 3, 40} {
		if got := previewProductsPagination(n)["per_page"]; got != 15 {
			t.Errorf("itemCount %d: per_page = %v, want 15", n, got)
		}
	}
}

// probeFields mirrors themefs/fixtures_test.go's fixtureProbe: a Liquid reference to every leaf path in v.
func probeFields(b *strings.Builder, path string, v any, depth int) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			probeFields(b, path+"."+k, x[k], depth)
		}
	case []any:
		alias := fmt.Sprintf("x%d", depth)
		fmt.Fprintf(b, "{%% for %s in %s %%}\n", alias, path)
		if len(x) > 0 {
			probeFields(b, alias, x[0], depth+1)
		}
		b.WriteString("{% endfor %}\n")
	default:
		fmt.Fprintf(b, "{{ %s }}\n", path)
	}
}

func TestPreviewProductItems_PassKnownFields(t *testing.T) {
	sku, barcode, compare := "RP-1", "1234567890123", 15.0
	products := map[string]any{
		"items": previewProductItems([]themefs.Product{{
			ID: 1, Name: "Real Widget", Slug: "real-widget", Description: "Live.", Price: 12.5, ComparePrice: &compare,
			SKU: &sku, Barcode: &barcode, Attachments: []themefs.ProductAttachment{{URL: "https://cdn.example.com/rp1.jpg"}},
			DefaultVariant: &themefs.ProductVariantRef{ID: 42},
		}}),
		"pagination": previewProductsPagination(1),
	}

	var b strings.Builder
	probeFields(&b, "products", products, 0)
	if !strings.Contains(b.String(), "x0.can_quick_add") {
		t.Fatalf("probe did not reach the mapped item fields; got:\n%s", b.String())
	}

	p := themecheck.Proposal{Files: []themecheck.ProposedFile{{Path: "components/products-probe.liquid", Content: b.String()}}}
	for _, f := range themecheck.Check(p, themecheck.Snapshot{}) {
		if f.Rule == "known-fields" {
			t.Errorf("mapped product field rejected by known-fields: %s", f.Message)
		}
	}
}

// External test package: themecheck imports themefs, so an internal test importing themecheck would be a cycle.
package themefs_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"ai-chat/internal/liquidrender"
	"ai-chat/internal/themecheck"
	"ai-chat/internal/themefs"
)

// fixtureProbe emits a Liquid reference to every leaf path in v, looping over arrays the way a template would.
func fixtureProbe(b *strings.Builder, path string, v any, depth int) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			// variants[].options is a map keyed by choice-type id, read by subscript, not as named fields.
			if strings.HasSuffix(path, ".options") {
				fmt.Fprintf(b, "{{ %s['%s'] }}\n", path, k)
				continue
			}
			fixtureProbe(b, path+"."+k, x[k], depth)
		}
	case []any:
		alias := fmt.Sprintf("x%d", depth)
		fmt.Fprintf(b, "{%% for %s in %s %%}\n", alias, path)
		if len(x) > 0 {
			fixtureProbe(b, alias, x[0], depth+1)
		}
		b.WriteString("{% endfor %}\n")
	default:
		fmt.Fprintf(b, "{{ %s }}\n", path)
	}
}

func TestFixtureContext_PassesKnownFields(t *testing.T) {
	ctx := themefs.FixtureContext()
	roots := make([]string, 0, len(ctx))
	for k := range ctx {
		roots = append(roots, k)
	}
	sort.Strings(roots)

	var b strings.Builder
	for _, root := range roots {
		fixtureProbe(&b, root, ctx[root], 0)
	}
	// Every basket line's optional fields, not just items[0]'s: the probe only walks the first element of each array.
	for i := range themefs.FixtureBasket()["items"].([]any) {
		fixtureProbe(&b, fmt.Sprintf("basket.items[%d]", i), themefs.FixtureBasket()["items"].([]any)[i], 0)
	}

	p := themecheck.Proposal{Files: []themecheck.ProposedFile{{Path: "components/fixture-probe.liquid", Content: b.String()}}}
	for _, f := range themecheck.Check(p, themecheck.Snapshot{}) {
		if f.Rule == "known-fields" {
			t.Errorf("fixture field rejected by known-fields: %s", f.Message)
		}
	}
	if !strings.Contains(b.String(), "basket.items[1].extensions_data.addons") {
		t.Fatalf("probe did not reach the add-on basket line; got:\n%s", b.String())
	}
}

func TestFixtureProduct_PricesArePounds(t *testing.T) {
	product := themefs.FixtureProduct()
	// §1: `12.5 | money` -> £12.50, so a pounds amount must format back to its own price_formatted.
	if got := fmt.Sprintf("£%.2f", product["price_amount"]); got != product["price_formatted"] {
		t.Errorf("product price_amount %v formats as %s, want %s", product["price_amount"], got, product["price_formatted"])
	}
	for _, v := range product["variants"].([]any) {
		variant := v.(map[string]any)
		if got := fmt.Sprintf("£%.2f", variant["price_amount"]); got != variant["price_formatted"] {
			t.Errorf("variant %v price_amount %v formats as %s, want %s", variant["id"], variant["price_amount"], got, variant["price_formatted"])
		}
	}
}

func TestFixtureProduct_InternallyConsistent(t *testing.T) {
	product := themefs.FixtureProduct()
	variants := product["variants"].([]any)

	if product["variant_count"] != len(variants) {
		t.Errorf("variant_count = %v, want %d", product["variant_count"], len(variants))
	}
	if product["title"] != product["name"] {
		t.Errorf("title %v != name %v", product["title"], product["name"])
	}
	if product["can_quick_add"] != product["show_add_to_cart"] {
		t.Errorf("can_quick_add %v != show_add_to_cart %v", product["can_quick_add"], product["show_add_to_cart"])
	}

	var decoded, want []any
	if err := json.Unmarshal([]byte(product["variants_json"].(string)), &decoded); err != nil {
		t.Fatalf("variants_json does not decode: %v", err)
	}
	raw, _ := json.Marshal(variants)
	_ = json.Unmarshal(raw, &want)
	if !reflect.DeepEqual(decoded, want) {
		t.Errorf("variants_json does not match variants:\n got %v\nwant %v", decoded, want)
	}

	choiceItems := map[string]map[string]bool{}
	for _, c := range product["choices"].([]any) {
		choice := c.(map[string]any)
		items := map[string]bool{}
		for _, i := range choice["items"].([]any) {
			items[i.(map[string]any)["id"].(string)] = true
		}
		choiceItems[choice["id"].(string)] = items
	}
	for _, v := range variants {
		for typeID, itemID := range v.(map[string]any)["options"].(map[string]any) {
			if !choiceItems[typeID][itemID.(string)] {
				t.Errorf("variant option %s -> %v does not match any choice item", typeID, itemID)
			}
		}
	}

	var grouped []map[string]any
	for _, g := range product["addon_groups"].([]any) {
		group := g.(map[string]any)
		for _, a := range group["addons"].([]any) {
			addon := map[string]any{"group_id": group["id"], "group_name": group["name"]}
			for k, val := range a.(map[string]any) {
				addon[k] = val
			}
			grouped = append(grouped, addon)
		}
	}
	flat := product["addons"].([]any)
	if len(flat) != len(grouped) {
		t.Fatalf("flattened addons has %d entries, groups hold %d", len(flat), len(grouped))
	}
	for i := range flat {
		if !reflect.DeepEqual(flat[i], any(grouped[i])) {
			t.Errorf("addons[%d] = %v, want %v", i, flat[i], grouped[i])
		}
	}
	if !reflect.DeepEqual(product["addon_groups"], product["add_on_groups"]) {
		t.Error("add_on_groups must be the same value as addon_groups")
	}
}

func TestFixtureBasket_CartTemplateRendersVariantAndAddon(t *testing.T) {
	cart := `{% if basket %}{% for item in basket.items %}[{{ item.name }} x{{ item.quantity }}
{% if item.variant %}{% for opt in item.variant.items %}{{ opt.choice_type_label }}: {{ opt.name }}{% endfor %}{% endif %}
{% if item.extensions_data %}{% for addon in item.extensions_data.addons %}+ {{ addon.name }}{% endfor %}{% endif %}]
{% endfor %}{% endif %}`
	r := &liquidrender.Renderer{Files: map[string]string{"pages/cart.liquid": cart}}
	html, errs := r.Render("pages/cart.liquid", themefs.FixtureContext())
	if len(errs) != 0 {
		t.Fatalf("render errors: %v", errs)
	}
	for _, want := range []string{"Size: 30g", "+ Gift wrap", "Sample Tote x1"} {
		if !strings.Contains(html, want) {
			t.Errorf("expected cart render to contain %q, got:\n%s", want, html)
		}
	}
}

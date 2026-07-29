package themecheck

import (
	"reflect"
	"testing"
)

func TestScanTags(t *testing.T) {
	content := "{% if x == true %}\n{{ y }}\n{%- comment -%}hi{%- endcomment -%}\n{% endif %}"
	tags := ScanTags(content)
	if len(tags) != 4 {
		t.Fatalf("expected 4 tags, got %d: %+v", len(tags), tags)
	}
	if tags[0].Name != "if" || tags[0].Raw != "x == true" || tags[0].Line != 1 {
		t.Errorf("unexpected tag[0]: %+v", tags[0])
	}
	if tags[1].Name != "comment" || tags[1].Raw != "" {
		t.Errorf("unexpected tag[1]: %+v", tags[1])
	}
	if tags[2].Name != "endcomment" || tags[2].Raw != "" {
		t.Errorf("unexpected tag[2]: %+v", tags[2])
	}
	if tags[3].Name != "endif" || tags[3].Line != 4 {
		t.Errorf("unexpected tag[3]: %+v", tags[3])
	}
}

func TestParseExpression(t *testing.T) {
	cases := []struct {
		raw      string
		wantPath string
		wantFilt []string
	}{
		{"theme", "theme", nil},
		{"product.choices", "product.choices", nil},
		{"'components/header'", "", nil},
		{"0", "", nil},
		{"true", "", nil},
		{"product.price_amount | default: 0", "product.price_amount", []string{"default"}},
		{"products.items | size", "products.items", []string{"size"}},
		{"name | upcase | strip", "name", []string{"upcase", "strip"}},
	}
	for _, c := range cases {
		got := ParseExpression(c.raw)
		if got.Path != c.wantPath {
			t.Errorf("ParseExpression(%q).Path = %q, want %q", c.raw, got.Path, c.wantPath)
		}
		sameFilters := reflect.DeepEqual(got.Filters, c.wantFilt) || (len(got.Filters) == 0 && len(c.wantFilt) == 0)
		if !sameFilters {
			t.Errorf("ParseExpression(%q).Filters = %v, want %v", c.raw, got.Filters, c.wantFilt)
		}
	}
}

func TestParseRenderTag(t *testing.T) {
	raw := "'liquid/layout-start', page: page, store: store, customer_authenticated: auth_check"
	target, params, ok := ParseRenderTag(raw)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if target != "liquid/layout-start" {
		t.Errorf("target = %q", target)
	}
	want := []RenderParam{
		{Key: "page", Value: "page"},
		{Key: "store", Value: "store"},
		{Key: "customer_authenticated", Value: "auth_check"},
	}
	if !reflect.DeepEqual(params, want) {
		t.Errorf("params = %+v, want %+v", params, want)
	}

	if _, _, ok := ParseRenderTag("theme"); ok {
		t.Errorf("expected ok=false for a non-literal first argument")
	}
}

func TestSplitForTag(t *testing.T) {
	varName, source, ok := splitForTag("choice in product.choices")
	if !ok || varName != "choice" || source != "product.choices" {
		t.Errorf("splitForTag = (%q, %q, %v)", varName, source, ok)
	}
	if _, _, ok := splitForTag("not a for tag"); ok {
		t.Errorf("expected ok=false for a malformed for-tag body")
	}
}

func TestScanIfConditions(t *testing.T) {
	cases := []struct {
		raw         string
		wantBare    bool
		wantGuarded bool
		wantRefs    []string
	}{
		{"product.on_sale", true, false, []string{"product.on_sale"}},
		{"product.on_sale == true or product.on_sale == 1", false, true, []string{"product.on_sale"}},
		{"product.on_sale == 1 or product.on_sale == true", false, true, []string{"product.on_sale"}},
		{"x != blank", false, false, []string{"x"}},
		{"x == true", false, false, []string{"x"}},
	}
	for _, c := range cases {
		content := "{% if " + c.raw + " %}{% endif %}"
		conds := ScanIfConditions(content)
		if len(conds) != 1 {
			t.Fatalf("ScanIfConditions(%q): expected 1 condition, got %d", c.raw, len(conds))
		}
		got := conds[0]
		if got.Bare != c.wantBare {
			t.Errorf("%q: Bare = %v, want %v", c.raw, got.Bare, c.wantBare)
		}
		if got.GuardsBoolIsh != c.wantGuarded {
			t.Errorf("%q: GuardsBoolIsh = %v, want %v", c.raw, got.GuardsBoolIsh, c.wantGuarded)
		}
		if !reflect.DeepEqual(got.Refs, c.wantRefs) {
			t.Errorf("%q: Refs = %v, want %v", c.raw, got.Refs, c.wantRefs)
		}
	}
}

func TestSplitTopLevel(t *testing.T) {
	got := splitTopLevel("'a, b', key: value, other: 'x, y'", ',')
	want := []string{"'a, b'", " key: value", " other: 'x, y'"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitTopLevel = %#v, want %#v", got, want)
	}
}

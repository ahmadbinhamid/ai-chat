package themecheck

import (
	"strings"
	"testing"
)

func ifTag(raw string) Tag { return Tag{Name: "if", Raw: " " + raw + " ", Line: 1} }

func TestParseIfCondition_Operators(t *testing.T) {
	tests := []struct {
		raw      string
		wantRefs []string
		wantBare bool
	}{
		{"products.pagination.total > 0", []string{"products.pagination.total"}, false},
		{"products.pagination.total >= 1", []string{"products.pagination.total"}, false},
		{"product.price_amount < 10", []string{"product.price_amount"}, false},
		{"product.price_amount <= 10", []string{"product.price_amount"}, false},
		{"product.name != blank", []string{"product.name"}, false},
		{"product.name <> empty", []string{"product.name"}, false},
		{"product.name contains 'cream'", []string{"product.name"}, false},
		{"product.price_amount > product.variants.first.price_amount", []string{"product.price_amount", "product.variants.first.price_amount"}, false},
		{"product.id == nil", []string{"product.id"}, false},
		{"product.id == true", []string{"product.id"}, false},
		{"product.name", []string{"product.name"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got := parseIfCondition(ifTag(tt.raw))
			if strings.Join(got.Refs, "|") != strings.Join(tt.wantRefs, "|") || got.Bare != tt.wantBare || got.Filtered {
				t.Errorf("parseIfCondition(%q) = refs %q bare %v filtered %v; want refs %q bare %v",
					tt.raw, got.Refs, got.Bare, got.Filtered, tt.wantRefs, tt.wantBare)
			}
		})
	}
}

func TestParseIfCondition_AndOrAndQuotes(t *testing.T) {
	tests := []struct {
		raw      string
		wantRefs []string
		wantBare bool
	}{
		{"product.on_sale and product.has_variants", []string{"product.on_sale", "product.has_variants"}, true},
		{"product.name != blank and products.pagination.total > 0 or customer.name", []string{"product.name", "products.pagination.total", "customer.name"}, false},
		{"product.name == 'salt and pepper'", []string{"product.name"}, false},
		{"product.name == \"this or that\"", []string{"product.name"}, false},
		{"product.name contains 'a > b'", []string{"product.name"}, false},
		{"'a > b' == product.name", []string{"product.name"}, false},
		{"brand.name", []string{"brand.name"}, true}, // "and" inside a word is not a split
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got := parseIfCondition(ifTag(tt.raw))
			if strings.Join(got.Refs, "|") != strings.Join(tt.wantRefs, "|") || got.Bare != tt.wantBare {
				t.Errorf("parseIfCondition(%q) = refs %q bare %v; want refs %q bare %v", tt.raw, got.Refs, got.Bare, tt.wantRefs, tt.wantBare)
			}
		})
	}
}

func TestParseIfCondition_BoolGuardUnchanged(t *testing.T) {
	tests := []struct {
		raw       string
		wantGuard bool
		wantBare  bool
	}{
		{"product.on_sale == true or product.on_sale == 1", true, false},
		{"product.on_sale == 1 or product.on_sale == true", true, false},
		{"product.on_sale > 0", false, false},
		{"product.on_sale", false, true},
		{"product.on_sale == true", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got := parseIfCondition(ifTag(tt.raw))
			if got.GuardsBoolIsh != tt.wantGuard || got.Bare != tt.wantBare {
				t.Errorf("parseIfCondition(%q) = guard %v bare %v; want guard %v bare %v", tt.raw, got.GuardsBoolIsh, got.Bare, tt.wantGuard, tt.wantBare)
			}
		})
	}
	// x > 0 must not be treated as a bare truthy check by the rule itself.
	p := Proposal{Files: []ProposedFile{{Path: "pages/x.liquid", Content: "{% if product.on_sale > 0 %}sale{% endif %}"}}}
	if got := checkBoolGuard(p, Snapshot{}); len(got) != 0 {
		t.Errorf("a comparison is not a bare check, got %+v", got)
	}
}

func TestKnownFields_ConditionsAndBuiltins(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantMsg string // "" means no finding
	}{
		{name: "shop page line 208", content: "{% if products.pagination.total > 0 %}<p>{{ products.pagination.total }} products</p>{% endif %}"},
		{name: "each operator on a known field", content: "{% if products.pagination.page >= 1 and products.pagination.page <= products.pagination.last_page %}x{% endif %}" +
			"{% if product.price_amount < 5 or product.name contains 'kit' %}y{% endif %}"},
		{name: "unknown field on the right side", content: "{% if product.price_amount > product.cost %}x{% endif %}", wantMsg: "'product.cost'"},
		{name: "unknown field after and", content: "{% if product.name and product.colour %}x{% endif %}", wantMsg: "'product.colour'"},
		{name: "list size in a condition", content: "{% if products.items.size > 0 %}x{% endif %}"},
		{name: "string size", content: "{% if product.name.size > 20 %}{{ product.name.size }}{% endif %}"},
		{name: "first and last resolve to the item", content: "{{ products.items.first.name }} {{ products.items.last.image_url }}"},
		{name: "first still checks item fields", content: "{{ products.items.first.colour }}", wantMsg: "'products.items.first.colour'"},
		{name: "size on an object is still unknown", content: "{{ products.pagination.size }}", wantMsg: "pagination"},
		{name: "filter inside if", content: "{% if products.items | size > 0 %}x{% endif %}",
			wantMsg: "filters don't work inside {% if %} — use products.items.size > 0, or assign the value first"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkKnownFieldsInFile("pages/shop.liquid", tt.content)
			if tt.wantMsg == "" {
				if len(got) != 0 {
					t.Errorf("expected no findings, got %+v", got)
				}
				return
			}
			if len(got) != 1 || !strings.Contains(got[0].Message, tt.wantMsg) {
				t.Errorf("expected one finding containing %q, got %+v", tt.wantMsg, got)
			}
		})
	}
}

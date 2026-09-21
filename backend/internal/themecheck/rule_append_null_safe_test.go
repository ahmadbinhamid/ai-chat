package themecheck

import "testing"

func TestCheckAppendNullSafe_FlagsBareAppend(t *testing.T) {
	content := `{% assign all_slug = settings.productList.defaultCategorySlug %}
{% if all_slug == blank %}
  {% assign all_slug = 'all' %}
{% endif %}
{% assign all_url = '/shop?category=' | append: all_slug %}
`
	p := Proposal{Files: []ProposedFile{{Path: "pages/products.liquid", Content: content}}}
	got := checkAppendNullSafe(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding, got %+v", got)
	}
	if got[0].Rule != ruleIDAppendNullSafe {
		t.Fatalf("wrong rule: %+v", got[0])
	}
}

func TestCheckAppendNullSafe_OKWithDefault(t *testing.T) {
	content := `{% assign all_slug = settings.productList.defaultCategorySlug | default: 'all' %}
{% assign all_url = '/shop?category=' | append: all_slug %}
`
	p := Proposal{Files: []ProposedFile{{Path: "pages/products.liquid", Content: content}}}
	if got := checkAppendNullSafe(p, Snapshot{}); len(got) != 0 {
		t.Fatalf("expected clean, got %+v", got)
	}
}

func TestCheckAppendNullSafe_OKLiteralAppend(t *testing.T) {
	content := `{% assign all_url = '/shop?category=' | append: 'all' %}`
	p := Proposal{Files: []ProposedFile{{Path: "pages/products.liquid", Content: content}}}
	if got := checkAppendNullSafe(p, Snapshot{}); len(got) != 0 {
		t.Fatalf("literal append arg must not flag, got %+v", got)
	}
}

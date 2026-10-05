package themecheck

import "testing"

func TestCheckKnownFields_ValidDirectFields(t *testing.T) {
	content := `{{ product.name }} {{ page.title }} {{ store.name }} {{ theme.asset_base }}
{% if product.on_sale == true or product.on_sale == 1 %}sale{% endif %}`
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: content}}}
	if got := checkKnownFields(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckKnownFields_UnknownRootIsSkipped(t *testing.T) {
	// "variant"/"background" etc. are component render params (§1), never
	// §7 objects — an unrecognized root must never be flagged.
	content := `{{ variant.label }} {% if background %}x{% endif %}`
	p := Proposal{Files: []ProposedFile{{Path: "components/testimonials.liquid", Content: content}}}
	if got := checkKnownFields(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings for unknown roots, got %+v", got)
	}
}

func TestCheckKnownFields_KnownRootUnknownField(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: "{{ product.discount }}"}}}
	got := checkKnownFields(p, Snapshot{})
	if len(got) != 1 || got[0].Severity != SeverityError {
		t.Fatalf("expected 1 error finding, got %+v", got)
	}
}

func TestCheckKnownFields_ForloopFirstLastAllowed(t *testing.T) {
	content := `{% for item in products.items %}{% if forloop.first %}first{% endif %}{% if forloop.last %}last{% endif %}{% endfor %}`
	p := Proposal{Files: []ProposedFile{{Path: "pages/home.liquid", Content: content}}}
	if got := checkKnownFields(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckKnownFields_ForloopOtherFieldRejected(t *testing.T) {
	content := `{% for item in products.items %}{{ forloop.index }}{% endfor %}`
	p := Proposal{Files: []ProposedFile{{Path: "pages/home.liquid", Content: content}}}
	got := checkKnownFields(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for forloop.index, got %+v", got)
	}
}

func TestCheckKnownFields_ThemeColorsRejected(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: "{{ theme.colors.primary }}"}}}
	got := checkKnownFields(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for theme.colors.primary (only theme.asset_base is allowed), got %+v", got)
	}
}

func TestCheckKnownFields_OneHopLoopAlias(t *testing.T) {
	content := `{% for item in products.items %}{{ item.name }} {{ item.price_formatted }}{% endfor %}`
	p := Proposal{Files: []ProposedFile{{Path: "pages/home.liquid", Content: content}}}
	if got := checkKnownFields(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckKnownFields_OneHopLoopAliasUnknownField(t *testing.T) {
	content := `{% for item in products.items %}{{ item.made_up_field }}{% endfor %}`
	p := Proposal{Files: []ProposedFile{{Path: "pages/home.liquid", Content: content}}}
	got := checkKnownFields(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for an invented field on an aliased loop var, got %+v", got)
	}
}

func TestCheckKnownFields_ChainedTwoHopAlias(t *testing.T) {
	content := `{% for choice in product.choices %}{% for item in choice.items %}{{ item.name }}{% endfor %}{% endfor %}`
	p := Proposal{Files: []ProposedFile{{Path: "pages/product.liquid", Content: content}}}
	if got := checkKnownFields(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings for a chained two-level loop alias, got %+v", got)
	}
}

func TestCheckKnownFields_ChoiceLabelValidPriceInvalid(t *testing.T) {
	content := `{% for choice in product.choices %}{{ choice.label }} {{ choice.price }}{% endfor %}`
	p := Proposal{Files: []ProposedFile{{Path: "pages/product.liquid", Content: content}}}
	got := checkKnownFields(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for choice.price (not in the §7 shape), got %+v", got)
	}
}

func TestCheckKnownFields_FilterCategoriesIsItselfAnArray(t *testing.T) {
	content := `{% for c in filter_categories %}{{ c.slug }} {{ c.name }}{% endfor %}`
	p := Proposal{Files: []ProposedFile{{Path: "pages/products.liquid", Content: content}}}
	if got := checkKnownFields(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckKnownFields_UnknownLoopSourceSkipsAliasing(t *testing.T) {
	// The loop source's root is unknown, so it's skipped (no finding on the
	// loop itself), and the loop var never becomes a known alias either —
	content := `{% for x in something_unknown %}{{ x.whatever }}{% endfor %}`
	p := Proposal{Files: []ProposedFile{{Path: "components/widget.liquid", Content: content}}}
	if got := checkKnownFields(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckKnownFields_ReusedLoopVarAcrossSequentialLoops_MenuFirst(t *testing.T) {
	// Two sequential (not nested) loops reusing the short name "item" for
	// different sources — each must resolve against its own binding, not
	content := `{% for item in menu.items %}{{ item.active }}{% endfor %}
{% for item in products.items %}{{ item.price_formatted }}{% endfor %}`
	p := Proposal{Files: []ProposedFile{{Path: "components/header.liquid", Content: content}}}
	if got := checkKnownFields(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckKnownFields_ReusedLoopVarAcrossSequentialLoops_ProductsFirst(t *testing.T) {
	content := `{% for item in products.items %}{{ item.price_formatted }}{% endfor %}
{% for item in menu.items %}{{ item.active }}{% endfor %}`
	p := Proposal{Files: []ProposedFile{{Path: "components/header.liquid", Content: content}}}
	if got := checkKnownFields(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckKnownFields_LoopVarOutOfScopeAfterEndfor(t *testing.T) {
	// After the loop closes, "item" is no longer a known alias — a
	// reference to it outside the loop is an unknown root and must be
	content := `{% for item in products.items %}{{ item.name }}{% endfor %}{{ item.name }}`
	p := Proposal{Files: []ProposedFile{{Path: "pages/home.liquid", Content: content}}}
	if got := checkKnownFields(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings (out-of-scope ref is an unknown root, skipped), got %+v", got)
	}
}

func TestCheckKnownFields_IgnoresNonLiquidFiles(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/css/offers.css", Content: "{{ product.discount }}"}}}
	if got := checkKnownFields(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected non-.liquid files to be ignored, got %+v", got)
	}
}

func TestCheckKnownFields_EverySpecFieldPasses(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"product top-level", `{{ product.title }} {{ product.can_quick_add }} {{ product.show_add_to_cart }} {{ product.variant_count }}
{{ product.has_addons }} {{ product.name }} {{ product.slug }} {{ product.url }} {{ product.image_url }} {{ product.price_amount }}
{{ product.price_formatted }} {{ product.compare_at_price_formatted }} {{ product.on_sale }} {{ product.sku }} {{ product.barcode }}
{{ product.description }} {{ product.default_variant_id }} {{ product.has_variants }} {{ product.has_choices }} {{ product.variants_json }}
{% for img in product.images %}{{ img.url }}{% endfor %}`},
		{"product choices", `{% for c in product.choices %}{{ c.id }} {{ c.label }}{% for i in c.items %}{{ i.id }} {{ i.name }}{% endfor %}{% endfor %}`},
		{"product variants", `{% for v in product.variants %}{{ v.id }} {{ v.label }} {{ v.price_amount }} {{ v.price_formatted }}
{{ v.image_url }} {{ v.sku }} {{ v.barcode }} {{ v.is_available }} {{ v.options }}
{% for img in v.images %}{{ img.url }}{% endfor %}{% endfor %}`},
		{"addon groups", `{% for g in product.addon_groups %}{{ g.id }} {{ g.name }} {{ g.min_selection }} {{ g.max_selection }} {{ g.is_required }}
{% for a in g.addons %}{{ a.id }} {{ a.name }} {{ a.price_amount }} {{ a.price_formatted }} {{ a.max_quantity }} {{ a.is_active }}{% endfor %}{% endfor %}`},
		{"add_on_groups alias", `{% for g in product.add_on_groups %}{{ g.name }}{% for a in g.addons %}{{ a.price_formatted }}{% endfor %}{% endfor %}`},
		{"flattened addons", `{% for a in product.addons %}{{ a.id }} {{ a.name }} {{ a.price_amount }} {{ a.price_formatted }}
{{ a.max_quantity }} {{ a.is_active }} {{ a.group_id }} {{ a.group_name }}{% endfor %}`},
		{"products items", `{% for p in products.items %}{{ p.id }} {{ p.name }} {{ p.title }} {{ p.slug }} {{ p.url }} {{ p.image_url }}
{{ p.price_amount }} {{ p.price_formatted }} {{ p.compare_at_price_formatted }} {{ p.on_sale }} {{ p.sku }} {{ p.barcode }}
{{ p.description }} {{ p.default_variant_id }} {{ p.has_variants }} {{ p.can_quick_add }} {{ p.show_add_to_cart }}{% endfor %}
{{ products.pagination.page }} {{ products.pagination.last_page }} {{ products.pagination.total }} {{ products.pagination.per_page }}
{{ products.pagination.has_prev }} {{ products.pagination.has_next }} {{ products.pagination.prev_page }} {{ products.pagination.next_page }}`},
		{"filters", `{{ filters.search }} {{ filters.sort }} {{ filters.category }} {{ filters.min_price }} {{ filters.max_price }} {{ filters.per_page }}`},
		{"basket", `{% if basket %}{{ basket.item_count }} {{ basket.sub_total }} {{ basket.total }} {{ basket.total_discount }}
{{ basket.shipping_charges }} {{ basket.customer_name }} {{ basket.customer_email }} {{ basket.customer_phone }}
{% for item in basket.items %}{{ item.id }} {{ item.variant_id }} {{ item.product_slug }} {{ item.name }} {{ item.note }}
{{ item.quantity }} {{ item.price }} {{ item.sub_total }} {{ item.total_discount }} {{ item.total }} {{ item.image_url }}
{% if item.variant %}{{ item.variant.id }}{% for o in item.variant.items %}{{ o.id }} {{ o.name }} {{ o.choice_type_label }}{% endfor %}{% endif %}
{% if item.extensions_data.addons %}{% for a in item.extensions_data.addons %}{{ a.id }} {{ a.name }} {{ a.price }} {{ a.quantity }}{% endfor %}{% endif %}
{% endfor %}{% endif %}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Proposal{Files: []ProposedFile{{Path: "pages/cart.liquid", Content: tt.content}}}
			if got := checkKnownFields(p, Snapshot{}); len(got) != 0 {
				t.Errorf("expected no findings, got %+v", got)
			}
		})
	}
}

func TestCheckKnownFields_InventedFieldsRejected(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"invented product field", `{{ product.not_a_real_field }}`},
		{"removed basket item price_formatted", `{% for item in basket.items %}{{ item.price_formatted }}{% endfor %}`},
		{"removed basket item total_formatted", `{% for item in basket.items %}{{ item.total_formatted }}{% endfor %}`},
		{"removed basket subtotal_formatted", `{{ basket.subtotal_formatted }}`},
		{"field after options subscript", `{% for v in product.variants %}{{ v.options[c.id].name }}{% endfor %}`},
		{"dotted field on options", `{% for v in product.variants %}{{ v.options.made_up }}{% endfor %}`},
		{"invented field inside subscript key", `{% for v in product.variants %}{{ v.options[product.not_a_real_field] }}{% endfor %}`},
		{"subscript on a named-field object", `{{ product[key] }}`},
		{"invented field after array index", `{{ product.images[0].not_a_real_field }}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Proposal{Files: []ProposedFile{{Path: "pages/cart.liquid", Content: tt.content}}}
			got := checkKnownFields(p, Snapshot{})
			if len(got) != 1 || got[0].Severity != SeverityError {
				t.Fatalf("expected 1 error finding, got %+v", got)
			}
		})
	}
}

func TestCheckKnownFields_VariantOptionsSubscriptAccess(t *testing.T) {
	content := `{% for variant in product.variants %}{% for choice in product.choices %}
{% if variant.options[choice.id] == item.id %}selected{% endif %}
<option data-id="{{ variant.options[choice.id] }}" data-raw="{{ variant.options['12'] }}">{{ variant.options[3] }}</option>
{% endfor %}{% endfor %}
{{ product.variants[0].options[choice.id] }} {{ product.images[0].url }}`
	p := Proposal{Files: []ProposedFile{{Path: "pages/product.liquid", Content: content}}}
	if got := checkKnownFields(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings for subscript access into variants[].options, got %+v", got)
	}
}

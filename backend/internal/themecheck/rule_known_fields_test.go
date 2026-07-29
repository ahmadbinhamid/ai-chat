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
	// any field access on it is likewise skipped as an unknown root.
	content := `{% for x in something_unknown %}{{ x.whatever }}{% endfor %}`
	p := Proposal{Files: []ProposedFile{{Path: "components/widget.liquid", Content: content}}}
	if got := checkKnownFields(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckKnownFields_ReusedLoopVarAcrossSequentialLoops_MenuFirst(t *testing.T) {
	// Two sequential (not nested) loops reusing the short name "item" for
	// different sources — each must resolve against its own binding, not
	// whichever one happened to be seen last across the whole file.
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
	// skipped, not resolved against the stale binding.
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

package themecheck

import "testing"

func TestCheckBoolGuard_ProperlyGuarded(t *testing.T) {
	content := `{% if product.on_sale == true or product.on_sale == 1 %}sale{% endif %}
{% if customer_authenticated == true or customer_authenticated == 1 %}hi{% endif %}`
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: content}}}
	if got := checkBoolGuard(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckBoolGuard_BareOnBoolField(t *testing.T) {
	cases := []string{
		"product.on_sale", "product.has_choices", "product.has_variants",
		"variant.is_available", "item.active", "customer_authenticated",
	}
	for _, ref := range cases {
		content := "{% if " + ref + " %}x{% endif %}"
		p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: content}}}
		got := checkBoolGuard(p, Snapshot{})
		if len(got) != 1 || got[0].Severity != SeverityError {
			t.Errorf("ref %q: expected 1 error finding, got %+v", ref, got)
		}
	}
}

func TestCheckBoolGuard_BareOnNonBoolFieldIsFine(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: "{% if product.description %}x{% endif %}"}}}
	if got := checkBoolGuard(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings for a bare check on a non-bool field, got %+v", got)
	}
}

func TestCheckBoolGuard_NotBlankGuardIsFine(t *testing.T) {
	// `!= blank` isn't the bool-ish true/1 guard, but it's also not a bare
	// truthy check, so rule 9 has nothing to say about it.
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: "{% if product.on_sale != blank %}x{% endif %}"}}}
	if got := checkBoolGuard(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckBoolGuard_IgnoresNonLiquidFiles(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/css/offers.css", Content: "{% if product.on_sale %}"}}}
	if got := checkBoolGuard(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected non-.liquid files to be ignored, got %+v", got)
	}
}

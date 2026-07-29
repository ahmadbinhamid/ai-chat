package themecheck

import "testing"

func TestCheckBalancedTags_Valid(t *testing.T) {
	content := `{% if x == true or x == 1 %}
  {% for item in products.items %}
    {% capture y %}{% comment %}note{% endcomment %}{% endcapture %}
  {% endfor %}
{% elsif z %}
  ok
{% else %}
  fallback
{% endif %}`
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: content}}}
	if got := checkBalancedTags(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckBalancedTags_Unclosed(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: "{% if x %}hi"}}}
	got := checkBalancedTags(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for an unclosed if, got %+v", got)
	}
}

func TestCheckBalancedTags_ExtraCloser(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: "hi{% endif %}"}}}
	got := checkBalancedTags(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for an unmatched endif, got %+v", got)
	}
}

func TestCheckBalancedTags_CrossedNesting(t *testing.T) {
	// {% for %} closes before the {% if %} it was opened inside of does —
	// the endif then closes the wrong (already-closed) block.
	content := "{% if x %}{% for i in y %}{% endif %}{% endfor %}"
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: content}}}
	got := checkBalancedTags(p, Snapshot{})
	// Two distinct real defects: the endif closes the wrong block (the for
	// is still open), and — because that endif is rejected rather than
	// popping the if — the if itself is left dangling unclosed too.
	if len(got) != 2 {
		t.Fatalf("expected 2 findings for crossed nesting, got %+v", got)
	}
}

func TestCheckBalancedTags_ElsifOutsideIf(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: "{% elsif x %}hi{% endif %}"}}}
	got := checkBalancedTags(p, Snapshot{})
	if len(got) == 0 {
		t.Fatalf("expected at least 1 finding for a stray elsif, got none")
	}
}

func TestCheckBalancedTags_IgnoresNonLiquidFiles(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/css/offers.css", Content: "{% if x %}"}}}
	if got := checkBalancedTags(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected non-.liquid files to be ignored, got %+v", got)
	}
}

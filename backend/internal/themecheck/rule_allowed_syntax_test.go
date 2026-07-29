package themecheck

import "testing"

func TestCheckAllowedSyntax_AllowedTagsAndFilters(t *testing.T) {
	content := `{% assign x = product.name | upcase | strip %}
{% if x != blank %}
{{ x | default: 'none' | asset_url }}
{% endif %}
{% for item in products.items %}
{% capture y %}hi{% endcapture %}
{% endfor %}
{% comment %}note{% endcomment %}`
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: content}}}
	if got := checkAllowedSyntax(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckAllowedSyntax_ForbiddenTags(t *testing.T) {
	for _, tag := range []string{"schema", "section", "include", "javascript", "stylesheet"} {
		content := "{% " + tag + " %}x{% end" + tag + " %}"
		p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: content}}}
		got := checkAllowedSyntax(p, Snapshot{})
		if len(got) == 0 {
			t.Errorf("tag %q: expected a finding, got none", tag)
			continue
		}
		if got[0].Rule != ruleIDAllowedSyntax || got[0].Severity != SeverityError {
			t.Errorf("tag %q: unexpected finding %+v", tag, got[0])
		}
	}
}

func TestCheckAllowedSyntax_UnknownTag(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: "{% unless x %}y{% endunless %}"}}}
	got := checkAllowedSyntax(p, Snapshot{})
	if len(got) != 2 { // "unless" and "endunless" are both not in the allowed set
		t.Fatalf("expected 2 findings for unless/endunless, got %+v", got)
	}
}

func TestCheckAllowedSyntax_UnknownFilter(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: "{{ product.name | truncate: 20 }}"}}}
	got := checkAllowedSyntax(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for the disallowed 'truncate' filter, got %+v", got)
	}
}

func TestCheckAllowedSyntax_IgnoresNonLiquidFiles(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/css/offers.css", Content: "{% schema %}"}}}
	if got := checkAllowedSyntax(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected non-.liquid files to be ignored, got %+v", got)
	}
}

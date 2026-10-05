package themecheck

import (
	"strings"
	"testing"
)

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
	// "replace" parses fine but isn't §1-documented — genuinely disallowed, unlike truncate below.
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: "{{ product.name | replace: 'a', 'b' }}"}}}
	got := checkAllowedSyntax(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for the disallowed 'replace' filter, got %+v", got)
	}
}

// TestCheckAllowedSyntax_SpecDocumentedFiltersAreAllowed covers the five §1-documented filters allowedFilters must accept.
func TestCheckAllowedSyntax_SpecDocumentedFiltersAreAllowed(t *testing.T) {
	content := `{{ price | money }}
{{ "slug-a,slug-b" | split: ',' | get_products }}
{{ product.name | escape }}
{{ product.description | strip_html }}
{{ product.description | truncate: 140 }}`
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: content}}}
	if got := checkAllowedSyntax(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings for spec-documented filters, got %+v", got)
	}
}

func TestCheckAllowedSyntax_IgnoresNonLiquidFiles(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/css/offers.css", Content: "{% schema %}"}}}
	if got := checkAllowedSyntax(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected non-.liquid files to be ignored, got %+v", got)
	}
}

func TestCheckAllowedSyntax_FindingsCarryLineAndOffset(t *testing.T) {
	content := "<div>\n  {% unless x %}y{% endunless %}\n  {{ product.name | replace: 'a', 'b' }}\n</div>"
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: content}}}
	got := checkAllowedSyntax(p, Snapshot{})
	want := []struct {
		line   int
		offset int
	}{
		{2, strings.Index(content, "{% unless")},
		{2, strings.Index(content, "{% endunless")},
		{3, strings.Index(content, "{{ product")},
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d findings, got %+v", len(want), got)
	}
	for i, w := range want {
		if got[i].Line != w.line || got[i].Offset != w.offset {
			t.Errorf("finding %d: got Line=%d Offset=%d, want Line=%d Offset=%d", i, got[i].Line, got[i].Offset, w.line, w.offset)
		}
	}
}

func TestCheckAllowedSyntax_PreExistingUnlessIsDowngraded(t *testing.T) {
	unless := "{% unless product.on_sale %}<span>Full price</span>{% endunless %}"
	baseline := "<div>\n" + unless + "\n</div>"
	tests := []struct {
		name     string
		baseline map[string]string
		want     Severity
	}{
		{"pre-existing line is downgraded", map[string]string{"liquid/layout-start.liquid": baseline}, SeverityWarning},
		{"newly introduced line stays an error", map[string]string{"liquid/layout-start.liquid": "<div>\n</div>"}, SeverityError},
		{"brand-new file stays an error", map[string]string{}, SeverityError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proposed := "<div>\n  <p>New banner</p>\n  " + unless + "\n</div>"
			p := Proposal{Files: []ProposedFile{{Path: "liquid/layout-start.liquid", Action: "update", Content: proposed}}}
			got := DowngradePreExistingFindings(checkAllowedSyntax(p, Snapshot{}), p, tt.baseline)
			if len(got) != 2 {
				t.Fatalf("expected 2 findings for unless/endunless, got %+v", got)
			}
			for _, f := range got {
				if f.Severity != tt.want {
					t.Errorf("expected severity %q, got %+v", tt.want, f)
				}
			}
		})
	}
}

func TestCheckAllowedSyntax_PlatformTagsAllowed(t *testing.T) {
	content := `{% content_for_header %}
{%- content_for_body -%}
{% content_for_footer %}
{% pay_later %}`
	p := Proposal{Files: []ProposedFile{{Path: "liquid/layout-start.liquid", Content: content}}}
	if got := checkAllowedSyntax(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected platform tags to pass, got %+v", got)
	}
}

func TestCheckAllowedSyntax_TagRejectionOmitsPlatformTags(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: "{% case x %}{% endcase %}"}}}
	got := checkAllowedSyntax(p, Snapshot{})
	if len(got) == 0 {
		t.Fatal("expected a finding for case")
	}
	for _, tag := range []string{"content_for_header", "content_for_body", "content_for_footer", "pay_later"} {
		if strings.Contains(got[0].Message, tag) {
			t.Errorf("tag rejection message must not teach platform tag %q: %s", tag, got[0].Message)
		}
	}
}

func TestCheckAllowedSyntax_FilterRejectionListsEveryAllowedFilter(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: "{{ product.name | replace: 'a', 'b' }}"}}}
	got := checkAllowedSyntax(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding, got %+v", got)
	}
	if len(allowedFilters) != 13 {
		t.Errorf("expected 13 allowed filters, got %d", len(allowedFilters))
	}
	const want = "asset_url, default, escape, get_products, money, plus, size, slice, split, strip, strip_html, truncate, upcase"
	if !strings.Contains(got[0].Message, "(§1: "+want+")") {
		t.Errorf("expected message to list all allowed filters %q, got %s", want, got[0].Message)
	}
}

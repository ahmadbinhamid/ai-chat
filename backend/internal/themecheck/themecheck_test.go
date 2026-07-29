package themecheck

import "testing"

// TestCheck_BadProposalProducesExactlyThreeErrors is phase 1's "Done when"
// acceptance scenario: a hand-written bad proposal with an invented field, a
// missing CSS link, and a bare bool guard produces exactly three error
// findings — and (by construction, since Check never touches disk) nothing
// is ever written for a proposal Check rejects.
func TestCheck_BadProposalProducesExactlyThreeErrors(t *testing.T) {
	pageContent := `{% render 'liquid/layout-start', page: page, store: store, menu: menu, path: path, theme: theme, customer: customer, customer_authenticated: auth_check, environment: environment, csrf_token: csrf_token %}
<section class="page-section">
  {% if product.on_sale %}Sale!{% endif %}
  <p>{{ product.discount }}</p>
</section>
{% render 'liquid/layout-end', theme: theme, store: store %}`

	proposal := Proposal{
		Files: []ProposedFile{
			{Path: "pages/offers.liquid", Action: "update", Content: pageContent},
			{Path: "components/css/new-widget.css", Action: "create", Content: ".x { padding: 4px; }"},
		},
	}
	snap := Snapshot{
		Files: map[string]string{
			"liquid/layout-start.liquid": "<html><head></head><body>",
			"liquid/layout-end.liquid":   "<main></main></body></html>",
		},
		Paths: map[string]bool{"liquid/layout-start.liquid": true, "liquid/layout-end.liquid": true},
	}

	findings := Check(proposal, snap)

	errorRules := map[string]bool{}
	for _, f := range findings {
		if f.Severity != SeverityError {
			t.Errorf("unexpected non-error finding: %+v", f)
			continue
		}
		errorRules[f.Rule] = true
	}
	if len(findings) != 3 {
		t.Fatalf("expected exactly 3 error findings, got %d: %+v", len(findings), findings)
	}
	for _, want := range []string{ruleIDAssetRegistered, ruleIDBoolGuard, ruleIDKnownFields} {
		if !errorRules[want] {
			t.Errorf("expected a finding from rule %q, got rules %v", want, errorRules)
		}
	}
}

func TestCheck_CleanProposalProducesNoFindings(t *testing.T) {
	pageContent := `{% render 'liquid/layout-start', page: page, store: store, menu: menu, path: path, theme: theme, customer: customer, customer_authenticated: auth_check, environment: environment, csrf_token: csrf_token %}
<section class="page-section">
  {% if product.on_sale == true or product.on_sale == 1 %}Sale!{% endif %}
  <p>{{ product.name }}</p>
</section>
{% render 'liquid/layout-end', theme: theme, store: store %}`

	proposal := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Action: "update", Content: pageContent}}}
	snap := Snapshot{
		Files: map[string]string{
			"liquid/layout-start.liquid": "<html><head></head><body>",
			"liquid/layout-end.liquid":   "<main></main></body></html>",
		},
		Paths: map[string]bool{"liquid/layout-start.liquid": true, "liquid/layout-end.liquid": true},
	}

	if got := Check(proposal, snap); len(got) != 0 {
		t.Errorf("expected no findings for a clean proposal, got %+v", got)
	}
}

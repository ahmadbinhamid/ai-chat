package themecheck

import (
	"strings"
	"testing"
)

func TestCheckPlaceholderBody_RejectsLiteralPlaceholder(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/faq.liquid", Action: "update", Content: validBoilerplateInlineWithBody("placeholder")}}}
	got := checkPlaceholderBody(p, Snapshot{})
	if len(got) != 1 || got[0].Rule != ruleIDPlaceholderBody {
		t.Fatalf("expected one placeholder-body finding, got %+v", got)
	}
}

func TestCheckPlaceholderBody_RejectsEmptyBody(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/faq.liquid", Action: "update", Content: validBoilerplateInlineWithBody("")}}}
	if got := checkPlaceholderBody(p, Snapshot{}); len(got) != 1 {
		t.Fatalf("expected one finding for an empty body, got %+v", got)
	}
}

func TestCheckPlaceholderBody_AllowsShortRealBody(t *testing.T) {
	// Short but genuine content (e.g. a promotional banner page) must never
	// be flagged — there's no length heuristic, only the exact-phrase match.
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Action: "update", Content: validBoilerplateInlineWithBody("Sale!")}}}
	if got := checkPlaceholderBody(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected short real content to be accepted, got findings: %+v", got)
	}
}

func TestCheckPlaceholderBody_AllowsRealProse(t *testing.T) {
	body := "<h1>FAQ</h1><p>Do you ship internationally? Yes, we ship worldwide.</p>"
	p := Proposal{Files: []ProposedFile{{Path: "pages/faq.liquid", Action: "update", Content: validBoilerplateInlineWithBody(body)}}}
	if got := checkPlaceholderBody(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected real prose to be accepted, got findings: %+v", got)
	}
}

func TestCheckPlaceholderBody_AllowsDynamicBinding(t *testing.T) {
	// A page whose only content is a {{ }} binding is real, page-specific content and must never be flagged.
	body := "<p>{{ product.name }}</p>"
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Action: "update", Content: validBoilerplateInlineWithBody(body)}}}
	if got := checkPlaceholderBody(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected a dynamic binding to be accepted, got findings: %+v", got)
	}
}

func TestCheckPlaceholderBody_AllowsComponentOnlyPage(t *testing.T) {
	// A page composed entirely of real component renders (the spec's preferred pattern) must never be flagged.
	body := "{% render 'components/store-hero-banner' %}\n{% render 'components/testimonials' %}"
	p := Proposal{Files: []ProposedFile{{Path: "pages/home.liquid", Action: "update", Content: validBoilerplateInlineWithBody(body)}}}
	if got := checkPlaceholderBody(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected a component-composed page to be accepted, got findings: %+v", got)
	}
}

func TestCheckPlaceholderBody_RejectsDrasticShrinkEvenWithoutKnownPhrase(t *testing.T) {
	// A stray "x" isn't a known placeholder phrase, but replacing a substantial page with it is the destructive pattern this rule catches.
	prev := validBoilerplateInlineWithBody(strings.Repeat("Real FAQ content. ", 20)) // well over 200 chars
	p := Proposal{Files: []ProposedFile{{Path: "pages/faq.liquid", Action: "update", Content: validBoilerplateInlineWithBody("x")}}}
	snap := Snapshot{Files: map[string]string{"pages/faq.liquid": prev}}

	got := checkPlaceholderBody(p, snap)
	if len(got) != 1 || got[0].Rule != ruleIDPlaceholderBody {
		t.Fatalf("expected one placeholder-body finding for a drastic shrink, got %+v", got)
	}
}

func TestCheckPlaceholderBody_ShrinkCheckIgnoresAlreadySmallPages(t *testing.T) {
	// A previously tiny page replaced with something tiny isn't this check's business — the exact-phrase/empty check covers that.
	prev := validBoilerplateInlineWithBody("Sale!") // well under 200 chars
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Action: "update", Content: validBoilerplateInlineWithBody("New sale!")}}}
	snap := Snapshot{Files: map[string]string{"pages/offers.liquid": prev}}

	if got := checkPlaceholderBody(p, snap); len(got) != 0 {
		t.Errorf("expected no finding when the previous page was already small, got %+v", got)
	}
}

func TestCheckPlaceholderBody_ShrinkCheckIgnoresCreateAction(t *testing.T) {
	// A brand-new page has no "before" to shrink from — the shrink check
	// must only ever apply to "update".
	prev := validBoilerplateInlineWithBody(strings.Repeat("Real content. ", 20))
	p := Proposal{Files: []ProposedFile{{Path: "pages/faq.liquid", Action: "create", Content: validBoilerplateInlineWithBody("Hi")}}}
	snap := Snapshot{Files: map[string]string{"pages/faq.liquid": prev}}

	if got := checkPlaceholderBody(p, snap); len(got) != 0 {
		t.Errorf("expected the shrink check to be skipped for a create action, got %+v", got)
	}
}

func TestCheckPlaceholderBody_IgnoresNonPageFiles(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "components/header.liquid", Action: "update", Content: "placeholder"}}}
	if got := checkPlaceholderBody(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected non-page files to be ignored, got findings: %+v", got)
	}
}

func validBoilerplateInlineWithBody(body string) string {
	return "{% render 'liquid/layout-start', page: page, store: store, menu: menu, path: path, theme: theme, customer: customer, customer_authenticated: auth_check, environment: environment, csrf_token: csrf_token %}\n" +
		body +
		"\n{% render 'liquid/layout-end', theme: theme, store: store %}"
}

package themecheck

import (
	"strings"
	"testing"
)

const validBoilerplateMultiline = `{% render 'liquid/layout-start',
  page: page,
  store: store,
  menu: menu,
  path: path,
  theme: theme,
  customer: customer,
  customer_authenticated: auth_check,
  environment: environment,
  csrf_token: csrf_token
%}
<section>hi</section>
{% render 'liquid/layout-end', theme: theme, store: store %}`

const validBoilerplateInline = `{% render 'liquid/layout-start', page: page, store: store, menu: menu, path: path, theme: theme, customer: customer, customer_authenticated: auth_check, environment: environment, csrf_token: csrf_token %}
<section>hi</section>
{% render 'liquid/layout-end', theme: theme, store: store %}`

func TestCheckPageBoilerplate_ValidMultiline(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Action: "create", Content: validBoilerplateMultiline}}}
	if got := checkPageBoilerplate(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings for valid multiline boilerplate, got %+v", got)
	}
}

func TestCheckPageBoilerplate_ValidInline(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/home.liquid", Action: "update", Content: validBoilerplateInline}}}
	if got := checkPageBoilerplate(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings for valid inline boilerplate, got %+v", got)
	}
}

func TestAutoFixMissingBoilerplate_AddsBothMissingRenders(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/faq.liquid", Action: "update", Content: "<section>hi</section>\n"}}}

	fixed, any := AutoFixMissingBoilerplate(p)
	if !any {
		t.Fatal("expected a fix to be applied")
	}
	patched, ok := fixed["pages/faq.liquid"]
	if !ok {
		t.Fatal("expected a patched entry for pages/faq.liquid")
	}

	// The patched content must itself pass the same rule it just fixed.
	p2 := Proposal{Files: []ProposedFile{{Path: "pages/faq.liquid", Action: "update", Content: patched}}}
	if got := checkPageBoilerplate(p2, Snapshot{}); len(got) != 0 {
		t.Errorf("expected patched content to pass checkPageBoilerplate, got findings: %+v", got)
	}
}

func TestAutoFixMissingBoilerplate_LeavesValidFileAlone(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/home.liquid", Action: "update", Content: validBoilerplateInline}}}
	if _, any := AutoFixMissingBoilerplate(p); any {
		t.Error("expected no fix for a file that already has both renders")
	}
}

func TestAutoFixMissingBoilerplate_ReplacesWrongParamsLayoutStart(t *testing.T) {
	// Reordered params: present but not the exact §3 call — the "page-creation
	// composed from scratch" failure mode this fix now also covers.
	content := `{% render 'liquid/layout-start', store: store, page: page, menu: menu, path: path, theme: theme, customer: customer, customer_authenticated: auth_check, environment: environment, csrf_token: csrf_token %}
<section>hi</section>
{% render 'liquid/layout-end', theme: theme, store: store %}`
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Action: "create", Content: content}}}

	fixed, any := AutoFixMissingBoilerplate(p)
	if !any {
		t.Fatal("expected a fix to be applied for wrong layout-start params")
	}
	patched, ok := fixed["pages/offers.liquid"]
	if !ok {
		t.Fatal("expected a patched entry for pages/offers.liquid")
	}
	if !strings.Contains(patched, layoutStartRenderTag) {
		t.Errorf("expected patched content to contain the canonical layout-start tag, got:\n%s", patched)
	}

	p2 := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Action: "update", Content: patched}}}
	if got := checkPageBoilerplate(p2, Snapshot{}); len(got) != 0 {
		t.Errorf("expected patched content to pass checkPageBoilerplate, got findings: %+v", got)
	}
}

func TestAutoFixMissingBoilerplate_ReplacesMissingParamLayoutEnd(t *testing.T) {
	// layout-end present but missing the store param.
	content := layoutStartRenderTag + `
<section>hi</section>
{% render 'liquid/layout-end', theme: theme %}`
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Action: "create", Content: content}}}

	fixed, any := AutoFixMissingBoilerplate(p)
	if !any {
		t.Fatal("expected a fix to be applied for a layout-end call missing a param")
	}
	patched, ok := fixed["pages/offers.liquid"]
	if !ok {
		t.Fatal("expected a patched entry for pages/offers.liquid")
	}
	if !strings.Contains(patched, layoutEndRenderTag) {
		t.Errorf("expected patched content to contain the canonical layout-end tag, got:\n%s", patched)
	}

	p2 := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Action: "update", Content: patched}}}
	if got := checkPageBoilerplate(p2, Snapshot{}); len(got) != 0 {
		t.Errorf("expected patched content to pass checkPageBoilerplate, got findings: %+v", got)
	}
}

func TestAutoFixMissingBoilerplate_FixesBothWrongParamsAtOnce(t *testing.T) {
	// Both render calls present but both malformed — a single call must fix both.
	content := `{% render 'liquid/layout-start', page: page, store: store, menu: menu, path: path, theme: theme, customer: customer, customer_authenticated: customer_authenticated, environment: environment, csrf_token: csrf_token %}
<section>hi</section>
{% render 'liquid/layout-end', store: store %}`
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Action: "create", Content: content}}}

	fixed, any := AutoFixMissingBoilerplate(p)
	if !any {
		t.Fatal("expected a fix to be applied when both renders have wrong params")
	}
	patched, ok := fixed["pages/offers.liquid"]
	if !ok {
		t.Fatal("expected a patched entry for pages/offers.liquid")
	}
	if !strings.Contains(patched, layoutStartRenderTag) || !strings.Contains(patched, layoutEndRenderTag) {
		t.Errorf("expected patched content to contain both canonical tags, got:\n%s", patched)
	}

	p2 := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Action: "update", Content: patched}}}
	if got := checkPageBoilerplate(p2, Snapshot{}); len(got) != 0 {
		t.Errorf("expected patched content to pass checkPageBoilerplate, got findings: %+v", got)
	}
}

func TestAutoFixMissingBoilerplate_IgnoresNonPageFiles(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "components/header.liquid", Action: "update", Content: "<div>no boilerplate needed here</div>"}}}
	if _, any := AutoFixMissingBoilerplate(p); any {
		t.Error("expected no fix for a non-pages/*.liquid file")
	}
}

func TestCheckPageBoilerplate_ValidAuthSubdir(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/auth/login.liquid", Action: "create", Content: validBoilerplateInline}}}
	if got := checkPageBoilerplate(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings for valid pages/auth file, got %+v", got)
	}
}

func TestCheckPageBoilerplate_MissingLayoutStart(t *testing.T) {
	content := `<section>hi</section>
{% render 'liquid/layout-end', theme: theme, store: store %}`
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Action: "create", Content: content}}}
	got := checkPageBoilerplate(p, Snapshot{})
	if len(got) != 1 || got[0].Severity != SeverityError {
		t.Fatalf("expected 1 error finding, got %+v", got)
	}
}

func TestCheckPageBoilerplate_MissingLayoutEnd(t *testing.T) {
	content := `{% render 'liquid/layout-start', page: page, store: store, menu: menu, path: path, theme: theme, customer: customer, customer_authenticated: auth_check, environment: environment, csrf_token: csrf_token %}
<section>hi</section>`
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Action: "create", Content: content}}}
	got := checkPageBoilerplate(p, Snapshot{})
	if len(got) != 1 || got[0].Severity != SeverityError {
		t.Fatalf("expected 1 error finding, got %+v", got)
	}
}

func TestCheckPageBoilerplate_MissingParam(t *testing.T) {
	content := `{% render 'liquid/layout-start', page: page, store: store, menu: menu, path: path, theme: theme, customer: customer, environment: environment, csrf_token: csrf_token %}
<section>hi</section>
{% render 'liquid/layout-end', theme: theme, store: store %}`
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Action: "create", Content: content}}}
	got := checkPageBoilerplate(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 error finding for a dropped param, got %+v", got)
	}
}

func TestCheckPageBoilerplate_ReorderedParams(t *testing.T) {
	content := `{% render 'liquid/layout-start', store: store, page: page, menu: menu, path: path, theme: theme, customer: customer, customer_authenticated: auth_check, environment: environment, csrf_token: csrf_token %}
{% render 'liquid/layout-end', theme: theme, store: store %}`
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Action: "create", Content: content}}}
	got := checkPageBoilerplate(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 error finding for reordered params, got %+v", got)
	}
}

func TestCheckPageBoilerplate_WrongCustomerAuthValue(t *testing.T) {
	content := `{% render 'liquid/layout-start', page: page, store: store, menu: menu, path: path, theme: theme, customer: customer, customer_authenticated: customer_authenticated, environment: environment, csrf_token: csrf_token %}
{% render 'liquid/layout-end', theme: theme, store: store %}`
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Action: "create", Content: content}}}
	got := checkPageBoilerplate(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 error finding for wrong customer_authenticated value, got %+v", got)
	}
}

func TestCheckPageBoilerplate_ExtraLayoutEndParam(t *testing.T) {
	content := `{% render 'liquid/layout-start', page: page, store: store, menu: menu, path: path, theme: theme, customer: customer, customer_authenticated: auth_check, environment: environment, csrf_token: csrf_token %}
{% render 'liquid/layout-end', theme: theme, store: store, extra: extra %}`
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Action: "create", Content: content}}}
	got := checkPageBoilerplate(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 error finding for an extra layout-end param, got %+v", got)
	}
}

func TestCheckPageBoilerplate_IgnoresNonPageFiles(t *testing.T) {
	p := Proposal{Files: []ProposedFile{
		{Path: "components/testimonials.liquid", Action: "update", Content: "no boilerplate here at all"},
		{Path: "pages/css/offers.css", Action: "create", Content: ".x { color: red; }"},
	}}
	if got := checkPageBoilerplate(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected non-page files to be ignored, got %+v", got)
	}
}

package liquidrender

import (
	"strings"
	"testing"
)

func render(t *testing.T, files map[string]string, entry string, vars map[string]any) (string, []string) {
	t.Helper()
	r := &Renderer{Files: files}
	return r.Render(entry, vars)
}

func TestRender_PlainTextAndOutput(t *testing.T) {
	files := map[string]string{"pages/home.liquid": "Hello {{ store.name }}!"}
	html, errs := render(t, files, "pages/home.liquid", map[string]any{"store": map[string]any{"name": "Acme"}})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if html != "Hello Acme!" {
		t.Errorf("got %q", html)
	}
}

func TestRender_Filters(t *testing.T) {
	files := map[string]string{"pages/home.liquid": "{{ name | upcase }} / {{ missing | default: 'none' }} / {{ 'x/img.png' | asset_url }}"}
	html, errs := render(t, files, "pages/home.liquid", map[string]any{"name": "cream"})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if html != "CREAM / none / /theme-assets/x/img.png" {
		t.Errorf("got %q", html)
	}
}

func TestRender_SliceFilterNegativeLengthDoesNotPanic(t *testing.T) {
	files := map[string]string{"pages/home.liquid": "[{{ 'hello' | slice: 1, -5 }}]"}
	html, errs := render(t, files, "pages/home.liquid", nil)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if html != "[]" {
		t.Errorf("expected a negative length to clamp to an empty slice, got %q", html)
	}
}

func TestRender_SliceFilterPositiveLength(t *testing.T) {
	files := map[string]string{"pages/home.liquid": "{{ 'hello world' | slice: 6, 5 }}"}
	html, errs := render(t, files, "pages/home.liquid", nil)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if html != "world" {
		t.Errorf("got %q", html)
	}
}

func TestRender_IfElsifElse(t *testing.T) {
	tpl := "{% if a == true or a == 1 %}A{% elsif b %}B{% else %}C{% endif %}"
	files := map[string]string{"pages/home.liquid": tpl}

	cases := []struct {
		vars map[string]any
		want string
	}{
		{map[string]any{"a": true, "b": false}, "A"},
		{map[string]any{"a": 1, "b": false}, "A"},
		{map[string]any{"a": false, "b": true}, "B"},
		{map[string]any{"a": false, "b": false}, "C"},
	}
	for _, c := range cases {
		html, errs := render(t, files, "pages/home.liquid", c.vars)
		if len(errs) != 0 {
			t.Fatalf("vars=%+v: unexpected errors: %v", c.vars, errs)
		}
		if html != c.want {
			t.Errorf("vars=%+v: got %q, want %q", c.vars, html, c.want)
		}
	}
}

func TestRender_NestedIfInsideSkippedBranch(t *testing.T) {
	// The skipped branch contains its own nested if/endif — skipBlock must
	// not mistake the inner endif for the outer one's boundary.
	tpl := "{% if false %}{% if true %}inner{% endif %}skipped{% else %}shown{% endif %}"
	files := map[string]string{"pages/home.liquid": tpl}
	html, errs := render(t, files, "pages/home.liquid", map[string]any{"false": false, "true": true})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if html != "shown" {
		t.Errorf("got %q", html)
	}
}

func TestRender_ForLoopWithForloopFirstLast(t *testing.T) {
	tpl := "{% for item in items %}{% if forloop.first %}[{% endif %}{{ item.name }}{% if forloop.last %}]{% else %},{% endif %}{% endfor %}"
	files := map[string]string{"pages/home.liquid": tpl}
	vars := map[string]any{"items": []any{
		map[string]any{"name": "a"}, map[string]any{"name": "b"}, map[string]any{"name": "c"},
	}}
	html, errs := render(t, files, "pages/home.liquid", vars)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if html != "[a,b,c]" {
		t.Errorf("got %q", html)
	}
}

func TestRender_ForLoopEmptySkipsBody(t *testing.T) {
	tpl := "before{% for item in items %}{{ item }}{% endfor %}after"
	files := map[string]string{"pages/home.liquid": tpl}
	html, errs := render(t, files, "pages/home.liquid", map[string]any{"items": []any{}})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if html != "beforeafter" {
		t.Errorf("got %q", html)
	}
}

func TestRender_AssignAndCapture(t *testing.T) {
	tpl := "{% assign x = 'hi' %}{{ x }}-{% capture y %}cap-{{ x }}{% endcapture %}{{ y }}"
	files := map[string]string{"pages/home.liquid": tpl}
	html, errs := render(t, files, "pages/home.liquid", map[string]any{})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if html != "hi-cap-hi" {
		t.Errorf("got %q", html)
	}
}

func TestRender_Comment(t *testing.T) {
	tpl := "before{% comment %}this is not rendered{% endcomment %}after"
	files := map[string]string{"pages/home.liquid": tpl}
	html, errs := render(t, files, "pages/home.liquid", map[string]any{})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if html != "beforeafter" {
		t.Errorf("got %q", html)
	}
}

func TestRender_RenderPartialWithExplicitParams(t *testing.T) {
	files := map[string]string{
		"pages/home.liquid":          "{% render 'components/greeting', who: customer.name %}",
		"components/greeting.liquid": "Hi {{ who }}! {{ unrelated }}",
	}
	vars := map[string]any{"customer": map[string]any{"name": "Sam"}, "unrelated": "should-not-leak"}
	html, errs := render(t, files, "pages/home.liquid", vars)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	// "unrelated" must NOT leak into the partial's scope (§1: only explicit
	// params are visible) — it renders as empty, not "should-not-leak".
	if html != "Hi Sam! " {
		t.Errorf("got %q", html)
	}
}

func TestRender_RenderTargetMissing(t *testing.T) {
	files := map[string]string{"pages/home.liquid": "{% render 'components/nonexistent' %}"}
	_, errs := render(t, files, "pages/home.liquid", map[string]any{})
	if len(errs) != 1 || !strings.Contains(errs[0], "components/nonexistent.liquid") {
		t.Fatalf("expected 1 missing-target error, got %v", errs)
	}
}

func TestRender_RenderPathMustHavePrefix(t *testing.T) {
	files := map[string]string{"pages/home.liquid": "{% render 'bare-name' %}"}
	_, errs := render(t, files, "pages/home.liquid", map[string]any{})
	if len(errs) != 1 {
		t.Fatalf("expected 1 error for a bare render path, got %v", errs)
	}
}

func TestRender_UnsupportedTagReportsErrorButContinues(t *testing.T) {
	tpl := "before{% schema %}after"
	files := map[string]string{"pages/home.liquid": tpl}
	html, errs := render(t, files, "pages/home.liquid", map[string]any{})
	if len(errs) != 1 || !strings.Contains(errs[0], "schema") {
		t.Fatalf("expected 1 unsupported-tag error, got %v", errs)
	}
	if html != "beforeafter" {
		t.Errorf("expected rendering to continue around the bad tag, got %q", html)
	}
}

func TestRender_UnsupportedFilterReportsErrorButContinues(t *testing.T) {
	tpl := "{{ name | truncate: 5 }}"
	files := map[string]string{"pages/home.liquid": tpl}
	html, errs := render(t, files, "pages/home.liquid", map[string]any{"name": "cream"})
	if len(errs) != 1 || !strings.Contains(errs[0], "truncate") {
		t.Fatalf("expected 1 unsupported-filter error, got %v", errs)
	}
	if html != "cream" {
		t.Errorf("expected the unfiltered value to still render, got %q", html)
	}
}

func TestRender_FullPageWithLayoutBoilerplate(t *testing.T) {
	files := map[string]string{
		"liquid/layout-start.liquid":     `<html><head><title>{{ page.seo_title }}</title></head><body>`,
		"liquid/layout-end.liquid":       `</body></html>`,
		"components/testimonials.liquid": `<section>{% for t in items %}{{ t }}{% endfor %}</section>`,
		"pages/home.liquid": `{% render 'liquid/layout-start', page: page, store: store, menu: menu, path: path, theme: theme, customer: customer, customer_authenticated: auth_check, environment: environment, csrf_token: csrf_token %}
<section class="sf-home">{{ store.name }}</section>
{% render 'liquid/layout-end', theme: theme, store: store %}`,
	}
	vars := map[string]any{
		"page":  map[string]any{"seo_title": "Home | Acme"},
		"store": map[string]any{"name": "Acme"},
		"menu":  map[string]any{"items": []any{}}, "path": "/", "theme": map[string]any{},
		"customer": map[string]any{}, "auth_check": true, "environment": "preview", "csrf_token": "x",
	}
	html, errs := render(t, files, "pages/home.liquid", vars)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if !strings.Contains(html, "<title>Home | Acme</title>") {
		t.Errorf("expected layout-start's title to render, got %q", html)
	}
	if !strings.Contains(html, `<section class="sf-home">Acme</section>`) {
		t.Errorf("expected the page body to render, got %q", html)
	}
	if !strings.HasSuffix(strings.TrimSpace(html), "</body></html>") {
		t.Errorf("expected layout-end to close the document, got %q", html)
	}
}

func TestRender_RenderCycleIsBounded(t *testing.T) {
	files := map[string]string{
		"components/a.liquid": "{% render 'components/b' %}",
		"components/b.liquid": "{% render 'components/a' %}",
	}
	_, errs := render(t, files, "components/a.liquid", map[string]any{})
	if len(errs) == 0 {
		t.Fatal("expected a render-depth-exceeded error for a render cycle")
	}
}

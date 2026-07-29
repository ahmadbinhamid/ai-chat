package themebuild

import (
	"context"
	"testing"

	"ai-chat/internal/ai"
	"ai-chat/internal/themecheck"
)

// fakeGenerator is a generator that never touches the real Claude API —
// checkAndRepair's retry loop is the one piece of this wiring that calls
// Generate more than once per turn, so it's the one piece that actually
// needs a fake rather than an httptest server (there's no HTTP boundary to
// intercept; ai.Generator wraps the Anthropic SDK client directly).
type fakeGenerator struct {
	calls   int
	results []*ai.Result // returned in order; the last one repeats once exhausted
}

func (f *fakeGenerator) Generate(_ context.Context, _ ai.ThemeContext, _ []ai.Turn, _ string, _ func(string), _ ai.ToolExecutor) (*ai.Result, error) {
	f.calls++
	idx := f.calls - 1
	if idx >= len(f.results) {
		idx = len(f.results) - 1
	}
	return f.results[idx], nil
}

const goodPageContent = `{% render 'liquid/layout-start', page: page, store: store, menu: menu, path: path, theme: theme, customer: customer, customer_authenticated: auth_check, environment: environment, csrf_token: csrf_token %}
<p>{{ product.name }}</p>
{% render 'liquid/layout-end', theme: theme, store: store %}`

const badPageContent = `{% render 'liquid/layout-start', page: page, store: store, menu: menu, path: path, theme: theme, customer: customer, customer_authenticated: auth_check, environment: environment, csrf_token: csrf_token %}
<p>{{ product.discount }}</p>
{% render 'liquid/layout-end', theme: theme, store: store %}`

func testSnapshot() themecheck.Snapshot {
	return themecheck.Snapshot{Paths: map[string]bool{
		"liquid/layout-start.liquid": true,
		"liquid/layout-end.liquid":   true,
	}}
}

func goodResult() *ai.Result {
	return &ai.Result{
		Summary:      "good",
		Files:        []ai.GeneratedFile{{Path: "pages/offers.liquid", Action: "update", Content: goodPageContent}},
		InputTokens:  20,
		OutputTokens: 10,
	}
}

func badResult() *ai.Result {
	return &ai.Result{
		Summary:      "bad",
		Files:        []ai.GeneratedFile{{Path: "pages/offers.liquid", Action: "update", Content: badPageContent}},
		InputTokens:  100,
		OutputTokens: 50,
	}
}

func TestCheckAndRepair_AcceptsCleanProposalWithoutRetrying(t *testing.T) {
	fg := &fakeGenerator{}
	svc := &Service{gen: fg}
	in := GenerateInput{TenantID: 1, ThemeSlug: "demo"}

	got, warnings, err := svc.checkAndRepair(context.Background(), in, "chat-1", ai.ThemeContext{}, nil, goodResult(), testSnapshot(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fg.calls != 0 {
		t.Errorf("expected no retry Generate calls for an already-clean proposal, got %d", fg.calls)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got %+v", warnings)
	}
	if got.Summary != "good" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestCheckAndRepair_RetriesOnceThenSucceeds(t *testing.T) {
	fg := &fakeGenerator{results: []*ai.Result{goodResult()}}
	svc := &Service{gen: fg}
	in := GenerateInput{TenantID: 1, ThemeSlug: "demo"}

	bad := badResult()
	got, warnings, err := svc.checkAndRepair(context.Background(), in, "chat-1", ai.ThemeContext{}, nil, bad, testSnapshot(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fg.calls != 1 {
		t.Errorf("expected exactly 1 retry Generate call, got %d", fg.calls)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got %+v", warnings)
	}
	if got.Summary != "good" {
		t.Fatalf("expected the retried (good) result to be returned, got %+v", got)
	}
	// Token usage from the rejected first attempt (100/50) must still be
	// folded into the accepted result's totals — otherwise the first
	// attempt's cost silently vanishes from what gets billed/recorded.
	if got.InputTokens != 120 || got.OutputTokens != 60 {
		t.Errorf("expected accumulated tokens 120/60, got %d/%d", got.InputTokens, got.OutputTokens)
	}
}

func TestCheckAndRepair_ExhaustsRetriesAndFails(t *testing.T) {
	fg := &fakeGenerator{results: []*ai.Result{badResult()}} // every retry comes back bad too
	svc := &Service{gen: fg}
	in := GenerateInput{TenantID: 1, ThemeSlug: "demo"}

	_, _, err := svc.checkAndRepair(context.Background(), in, "chat-1", ai.ThemeContext{}, nil, badResult(), testSnapshot(), nil, nil)
	if err == nil {
		t.Fatal("expected an error once retries are exhausted")
	}
	if fg.calls != maxThemeCheckRetries {
		t.Errorf("expected exactly %d retry Generate calls, got %d", maxThemeCheckRetries, fg.calls)
	}
}

func TestCheckAndRepair_WarningsPassThroughOnAccept(t *testing.T) {
	// A page missing a real SEO description (rule 7, warning-severity) still
	// gets accepted — warnings never block — but must be reported back.
	result := goodResult()
	result.PageRegistryEntry = nil // no page registration in this test, keep it simple; warning comes from elsewhere
	fg := &fakeGenerator{}
	svc := &Service{gen: fg}
	in := GenerateInput{TenantID: 1, ThemeSlug: "demo"}

	// Use a snapshot missing the theme-token var fallback to produce a
	// harmless warning-severity finding instead: add a CSS file with a
	// component-local custom property baking in a literal hex color.
	result.Files = append(result.Files, ai.GeneratedFile{
		Path: "components/css/testimonials.css", Action: "create", Content: ".x { --testimonials-accent: #ff6600; }",
	})
	result.LayoutLinksToAdd = []string{"components/css/testimonials.css"}

	got, warnings, err := svc.checkAndRepair(context.Background(), in, "chat-1", ai.ThemeContext{}, nil, result, testSnapshot(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fg.calls != 0 {
		t.Errorf("expected no retries (warnings don't block), got %d calls", fg.calls)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected exactly 1 warning finding, got %+v", warnings)
	}
	if got != result {
		t.Errorf("expected the same result object back when accepted on the first try")
	}
}

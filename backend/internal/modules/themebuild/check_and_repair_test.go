package themebuild

import (
	"context"
	"testing"

	"ai-chat/internal/ai"
	"ai-chat/internal/themecheck"
)

// Fake generator; multi-generate calls require fake, not httptest (SDK wraps client directly).
type fakeGenerator struct {
	calls           int
	results         []*ai.Result // returned in order; last repeats once exhausted
	visionSupported bool         // set true only for vision-specific tests
}

func (f *fakeGenerator) Generate(_ context.Context, _ ai.ThemeContext, _ []ai.Turn, _ string, _ []ai.Image, _ func(string), _ ai.ToolProgress, _ ai.ToolExecutor, _ ai.FileReader) (*ai.Result, error) {
	f.calls++
	idx := f.calls - 1
	if idx >= len(f.results) {
		idx = len(f.results) - 1
	}
	return f.results[idx], nil
}

func (f *fakeGenerator) SupportsVision() bool { return f.visionSupported }

// Satisfies interface; tests never reach summarizeHistoryThreshold.
func (f *fakeGenerator) Summarize(_ context.Context, turns []ai.Turn) (string, error) {
	return "", nil
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

	got, warnings, err := svc.checkAndRepair(context.Background(), in, "chat-1", ai.ThemeContext{}, nil, goodResult(), testSnapshot(), nil, nil, nil)
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
	got, warnings, err := svc.checkAndRepair(context.Background(), in, "chat-1", ai.ThemeContext{}, nil, bad, testSnapshot(), nil, nil, nil)
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
	// Token usage from first attempt must be folded into totals.
	if got.InputTokens != 120 || got.OutputTokens != 60 {
		t.Errorf("expected accumulated tokens 120/60, got %d/%d", got.InputTokens, got.OutputTokens)
	}
}

func TestCheckAndRepair_ExhaustsRetriesAndFails(t *testing.T) {
	fg := &fakeGenerator{results: []*ai.Result{badResult()}} // every retry comes back bad too
	svc := &Service{gen: fg}
	in := GenerateInput{TenantID: 1, ThemeSlug: "demo"}

	_, _, err := svc.checkAndRepair(context.Background(), in, "chat-1", ai.ThemeContext{}, nil, badResult(), testSnapshot(), nil, nil, nil)
	if err == nil {
		t.Fatal("expected an error once retries are exhausted")
	}
	if fg.calls != maxThemeCheckRetries {
		t.Errorf("expected exactly %d retry Generate calls, got %d", maxThemeCheckRetries, fg.calls)
	}
}

// Garbled repair reply; rejection consumes retry slot, doesn't kill generation.
func invalidResult() *ai.Result {
	return &ai.Result{
		Summary:      "garbled",
		Files:        []ai.GeneratedFile{{Path: "pages/offers.liquid", Action: "not-a-real-action", Content: badPageContent}},
		InputTokens:  30,
		OutputTokens: 15,
	}
}

func TestCheckAndRepair_RetriesPastAnInvalidRepairReply(t *testing.T) {
	// First repair attempt comes back malformed (rejected by validateProposal,
	// not themecheck); the second repair attempt is clean. With
	fg := &fakeGenerator{results: []*ai.Result{invalidResult(), goodResult()}}
	svc := &Service{gen: fg}
	in := GenerateInput{TenantID: 1, ThemeSlug: "demo"}

	got, _, err := svc.checkAndRepair(context.Background(), in, "chat-1", ai.ThemeContext{}, nil, badResult(), testSnapshot(), nil, nil, nil)
	if err != nil {
		t.Fatalf("expected the generation to recover after the invalid reply, got error: %v", err)
	}
	if fg.calls != 2 {
		t.Errorf("expected exactly 2 retry Generate calls (1 invalid + 1 good), got %d", fg.calls)
	}
	if got.Summary != "good" {
		t.Fatalf("expected the eventually-good result to be returned, got %+v", got)
	}
}

func TestCheckAndRepair_FailsWhenInvalidReplyExhaustsRetries(t *testing.T) {
	// Every repair attempt comes back malformed — must still fail cleanly
	// once the retry budget is exhausted, same as a themecheck rejection would.
	fg := &fakeGenerator{results: []*ai.Result{invalidResult()}}
	svc := &Service{gen: fg}
	in := GenerateInput{TenantID: 1, ThemeSlug: "demo"}

	_, _, err := svc.checkAndRepair(context.Background(), in, "chat-1", ai.ThemeContext{}, nil, badResult(), testSnapshot(), nil, nil, nil)
	if err == nil {
		t.Fatal("expected an error once retries are exhausted on repeated invalid replies")
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
	result.Files = append(result.Files, ai.GeneratedFile{
		Path: "components/css/testimonials.css", Action: "create", Content: ".x { --testimonials-accent: #ff6600; }",
	})
	result.LayoutLinksToAdd = []string{"components/css/testimonials.css"}

	got, warnings, err := svc.checkAndRepair(context.Background(), in, "chat-1", ai.ThemeContext{}, nil, result, testSnapshot(), nil, nil, nil)
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

// footerTrustpilotScript mirrors the incident this feature exists to
// prevent: a merchant's theme already carries a third-party <script src>
const footerTrustpilotScript = `  <script src="https://widget.trustpilot.com/tp-widget.min.js"></script>`

// TestCheckAndRepair_PreExistingScriptDoesNotTriggerRepair is the
// footer/"Powered By FlowPOS" incident end to end: the model's proposal
func TestCheckAndRepair_PreExistingScriptDoesNotTriggerRepair(t *testing.T) {
	baselineFooter := "<footer>\n" + footerTrustpilotScript + "\n</footer>"
	proposedFooter := "<footer>\n  <p>Powered by FlowPOS</p>\n" + footerTrustpilotScript + "\n</footer>"

	result := &ai.Result{
		Summary: "added Powered by FlowPOS",
		Files:   []ai.GeneratedFile{{Path: "components/footer.liquid", Action: "update", Content: proposedFooter}},
	}
	snap := themecheck.Snapshot{
		Paths: map[string]bool{"liquid/layout-start.liquid": true, "liquid/layout-end.liquid": true},
		Files: map[string]string{"components/footer.liquid": baselineFooter}, // the pre-change baseline buildSnapshot fetches
	}
	fg := &fakeGenerator{}
	svc := &Service{gen: fg}
	in := GenerateInput{TenantID: 1, ThemeSlug: "demo"}

	got, warnings, err := svc.checkAndRepair(context.Background(), in, "chat-1", ai.ThemeContext{}, nil, result, snap, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fg.calls != 0 {
		t.Fatalf("expected the pre-existing script to NOT trigger a repair round-trip, got %d Generate calls", fg.calls)
	}
	if len(warnings) != 1 || warnings[0].Rule != "no-framework" {
		t.Fatalf("expected the pre-existing violation to surface as exactly 1 no-framework warning, got %+v", warnings)
	}
	if got.Files[0].Content != proposedFooter {
		t.Errorf("expected the merchant's script to survive untouched in the accepted result, got %q", got.Files[0].Content)
	}
}

// TestCheckAndRepair_SameViolationInNewFileStillRepairs confirms the same
// off-theme script, when it's the model's OWN new file rather than an edit
func TestCheckAndRepair_SameViolationInNewFileStillRepairs(t *testing.T) {
	badNewFooter := "<footer>\n" + footerTrustpilotScript + "\n</footer>"
	fixedNewFooter := "<footer></footer>"

	first := &ai.Result{
		Summary: "new footer component",
		Files:   []ai.GeneratedFile{{Path: "components/new-footer.liquid", Action: "create", Content: badNewFooter}},
	}
	fixed := &ai.Result{
		Summary: "fixed",
		Files:   []ai.GeneratedFile{{Path: "components/new-footer.liquid", Action: "create", Content: fixedNewFooter}},
	}
	snap := themecheck.Snapshot{Paths: map[string]bool{"liquid/layout-start.liquid": true, "liquid/layout-end.liquid": true}}
	// No snap.Files entry for components/new-footer.liquid — it's brand new.

	fg := &fakeGenerator{results: []*ai.Result{fixed}}
	svc := &Service{gen: fg}
	in := GenerateInput{TenantID: 1, ThemeSlug: "demo"}

	got, _, err := svc.checkAndRepair(context.Background(), in, "chat-1", ai.ThemeContext{}, nil, first, snap, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fg.calls != 1 {
		t.Fatalf("expected a violation in a brand-new file to trigger exactly 1 repair round-trip, got %d", fg.calls)
	}
	if got.Summary != "fixed" {
		t.Fatalf("expected the repaired result to be returned, got %+v", got)
	}
}

// TestCheckAndRepair_HardcodedColorsAutoFixSkipsRepairRoundTrip is the case
// that motivates AutoFixThemeTokens: six hardcoded colors the model could
func TestCheckAndRepair_HardcodedColorsAutoFixSkipsRepairRoundTrip(t *testing.T) {
	defaultsJSON := `{"colors": {
		"primary": "#1e3a8a", "secondary": "#111111", "accent": "#3d5bbf",
		"background": "#ffffff", "border": "#e8e8e8", "danger": "#dc2626"
	}}`
	content := `.a { color: #1e3a8a; }
.b { color: #111111; }
.c { color: #3d5bbf; }
.d { background-color: #ffffff; }
.e { border-color: #e8e8e8; }
.f { color: #dc2626; }
`
	result := &ai.Result{
		Summary: "redesigned contact us page",
		Files:   []ai.GeneratedFile{{Path: "components/css/contact.css", Action: "create", Content: content}},
	}
	snap := themecheck.Snapshot{
		Paths: map[string]bool{"liquid/layout-start.liquid": true, "liquid/layout-end.liquid": true},
		Files: map[string]string{"defaults.json": defaultsJSON},
	}
	fg := &fakeGenerator{}
	svc := &Service{gen: fg}
	in := GenerateInput{TenantID: 1, ThemeSlug: "demo"}

	got, warnings, err := svc.checkAndRepair(context.Background(), in, "chat-1", ai.ThemeContext{}, nil, result, snap, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fg.calls != 0 {
		t.Fatalf("expected all six hardcoded colors to auto-fix without any repair round-trip, got %d Generate calls", fg.calls)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got %+v", warnings)
	}
	want := `.a { color: var(--theme-primary, #1e3a8a); }
.b { color: var(--theme-secondary, #111111); }
.c { color: var(--theme-accent, #3d5bbf); }
.d { background-color: var(--theme-background, #ffffff); }
.e { border-color: var(--theme-border, #e8e8e8); }
.f { color: var(--theme-danger, #dc2626); }
`
	if got.Files[0].Content != want {
		t.Errorf("got %q, want %q", got.Files[0].Content, want)
	}
}

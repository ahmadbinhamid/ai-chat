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

func (f *fakeGenerator) Generate(_ context.Context, _ ai.ThemeContext, _ []ai.Turn, _ string, _ func(string), _ ai.ToolProgress, _ ai.ToolExecutor, _ ai.FileReader) (*ai.Result, error) {
	f.calls++
	idx := f.calls - 1
	if idx >= len(f.results) {
		idx = len(f.results) - 1
	}
	return f.results[idx], nil
}

// Summarize satisfies the generator interface's history-summarization hook
// (see history_summary.go) — this fake never needs it for real, since
// these tests' history never exceeds summarizeHistoryThreshold turns.
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

	_, _, err := svc.checkAndRepair(context.Background(), in, "chat-1", ai.ThemeContext{}, nil, badResult(), testSnapshot(), nil, nil, nil)
	if err == nil {
		t.Fatal("expected an error once retries are exhausted")
	}
	if fg.calls != maxThemeCheckRetries {
		t.Errorf("expected exactly %d retry Generate calls, got %d", maxThemeCheckRetries, fg.calls)
	}
}

// invalidResult mimics a garbled/corrupted repair reply — e.g. the model's
// proposed path field coming back mangled — which validateProposal rejects
// outright (see service.go's checkAndRepair: a validateProposal failure
// during a retry must not immediately kill the whole generation, it should
// consume one of the same maxThemeCheckRetries slots as a themecheck
// rejection does).
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
	// maxThemeCheckRetries == 2, this must still succeed — the malformed
	// reply consumes a retry slot rather than hard-failing the generation.
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
	// component-local custom property baking in a literal hex color.
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
// (Trustpilot, Meta pixel, GA, Intercom, ...) before the model ever touches
// the file.
const footerTrustpilotScript = `  <script src="https://widget.trustpilot.com/tp-widget.min.js"></script>`

// TestCheckAndRepair_PreExistingScriptDoesNotTriggerRepair is the
// footer/"Powered By FlowPOS" incident end to end: the model's proposal
// re-emits the whole file (proposals are always complete files, never
// diffs) with the merchant's pre-existing script intact plus its own new
// line. Without pre-existing-violation filtering this used to cost a full
// repair round-trip whose only way to "fix" an error it didn't cause was to
// delete the merchant's script.
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
// to an existing one, still triggers a real repair — a brand-new file has
// no baseline, so the model owns every line of it.
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

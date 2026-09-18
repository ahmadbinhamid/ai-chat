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
	// visionSupported backs SupportsVision — zero-value false, matching
	// every existing test's expectation (none of them attach an image, so
	// none of them care); set true only in a test that specifically needs
	// Generate's own len(in.Images) > 0 && !SupportsVision() gate to pass.
	visionSupported bool
	lastTC          ai.ThemeContext
}

func (f *fakeGenerator) Generate(_ context.Context, tc ai.ThemeContext, _ []ai.Turn, _ string, _ []ai.Image, _ func(string), _ ai.ToolProgress, _ ai.ToolExecutor, _ ai.FileReader) (*ai.Result, error) {
	f.calls++
	f.lastTC = tc
	idx := f.calls - 1
	if idx >= len(f.results) {
		idx = len(f.results) - 1
	}
	return f.results[idx], nil
}

func (f *fakeGenerator) SupportsVision() bool { return f.visionSupported }

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
	tc := ai.ThemeContext{
		MaxTokensOverride: 24_000,
		MaxToolIterations: 28,
		FullHomeRedesign:  true,
		Metrics:           &ai.TurnMetrics{},
	}

	got, _, err := svc.checkAndRepair(context.Background(), in, "chat-1", tc, nil, first, snap, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fg.calls != 1 {
		t.Fatalf("expected a violation in a brand-new file to trigger exactly 1 repair round-trip, got %d", fg.calls)
	}
	if !fg.lastTC.Repair {
		t.Fatal("repair Generate must set ThemeContext.Repair")
	}
	if fg.lastTC.MaxTokensOverride != 0 {
		t.Fatalf("repair must not inherit MaxTokensOverride, got %d", fg.lastTC.MaxTokensOverride)
	}
	if fg.lastTC.MaxToolIterations != 0 || fg.lastTC.FullHomeRedesign {
		t.Fatalf("repair must strip full-page overrides, got iters=%d fullHome=%v",
			fg.lastTC.MaxToolIterations, fg.lastTC.FullHomeRedesign)
	}
	if got.Summary != "fixed" {
		t.Fatalf("expected the repaired result to be returned, got %+v", got)
	}
}

// TestCheckAndRepair_HardcodedColorsAutoFixSkipsRepairRoundTrip is the case
// that motivates AutoFixThemeTokens: six hardcoded colors the model could
// have reached for real defaults.json tokens for, all mechanically
// resolvable — the repair round-trip (a real Generate call, minutes of
// wall-clock in production) must never happen at all.
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

func fileByPath(files []ai.GeneratedFile, path string) (ai.GeneratedFile, bool) {
	for _, f := range files {
		if f.Path == path {
			return f, true
		}
	}
	return ai.GeneratedFile{}, false
}

func TestMergeRepairIntoProposal_ReplacesOnlyTouchedFiles(t *testing.T) {
	prior := &ai.Result{
		Summary: "full home",
		Files: []ai.GeneratedFile{
			{Path: "pages/home.liquid", Action: "update", Content: "HOME_V1"},
			{Path: "pages/css/home.css", Action: "create", Content: "CSS_BAD"},
			{Path: "components/store-hero-banner.liquid", Action: "update", Content: "HERO_V1"},
			{Path: "js/store-hero-banner.js", Action: "update", Content: "JS_V1"},
		},
		LayoutLinksToAdd: []string{"pages/css/home.css"},
	}
	repair := &ai.Result{
		Summary: "fixed tokens",
		Files: []ai.GeneratedFile{
			{Path: "pages/css/home.css", Action: "create", Content: "CSS_FIXED"},
		},
	}
	got := mergeRepairIntoProposal(prior, repair)
	if len(got.Files) != 4 {
		t.Fatalf("expected 4 files preserved, got %d: %v", len(got.Files), proposalPaths(got))
	}
	home, _ := fileByPath(got.Files, "pages/home.liquid")
	if home.Content != "HOME_V1" {
		t.Errorf("home.liquid should be untouched, got %q", home.Content)
	}
	css, _ := fileByPath(got.Files, "pages/css/home.css")
	if css.Content != "CSS_FIXED" {
		t.Errorf("home.css should be replaced, got %q", css.Content)
	}
	if got.Summary != "fixed tokens" {
		t.Errorf("expected repair summary, got %q", got.Summary)
	}
}

func TestMergeRepairIntoProposal_MultipleRepairFiles(t *testing.T) {
	prior := &ai.Result{
		Files: []ai.GeneratedFile{
			{Path: "pages/home.liquid", Action: "update", Content: "HOME"},
			{Path: "pages/css/home.css", Action: "create", Content: "CSS1"},
			{Path: "components/css/hero.css", Action: "create", Content: "CSS2"},
			{Path: "components/testimonials.liquid", Action: "create", Content: "TESTI"},
		},
	}
	repair := &ai.Result{
		Files: []ai.GeneratedFile{
			{Path: "pages/css/home.css", Action: "create", Content: "CSS1_FIXED"},
			{Path: "components/css/hero.css", Action: "create", Content: "CSS2_FIXED"},
		},
	}
	got := mergeRepairIntoProposal(prior, repair)
	if len(got.Files) != 4 {
		t.Fatalf("expected 4 files, got %d", len(got.Files))
	}
	css1, _ := fileByPath(got.Files, "pages/css/home.css")
	css2, _ := fileByPath(got.Files, "components/css/hero.css")
	testi, _ := fileByPath(got.Files, "components/testimonials.liquid")
	if css1.Content != "CSS1_FIXED" || css2.Content != "CSS2_FIXED" {
		t.Errorf("repaired CSS not applied: %q / %q", css1.Content, css2.Content)
	}
	if testi.Content != "TESTI" {
		t.Errorf("untouched file changed: %q", testi.Content)
	}
}

func TestMergeRepairIntoProposal_EmptyRepairKeepsPrior(t *testing.T) {
	prior := &ai.Result{
		Summary: "full home",
		Files: []ai.GeneratedFile{
			{Path: "pages/home.liquid", Action: "update", Content: "HOME"},
			{Path: "pages/css/home.css", Action: "create", Content: "CSS"},
		},
	}
	repair := &ai.Result{Summary: "could not fix", Files: nil, NeedsClarification: true}
	got := mergeRepairIntoProposal(prior, repair)
	if len(got.Files) != 2 {
		t.Fatalf("empty repair must keep prior files, got %d", len(got.Files))
	}
	if got.NeedsClarification {
		t.Error("empty subset repair must not mark merged result as needs_clarification")
	}
	if got.Summary != "could not fix" {
		t.Errorf("summary=%q", got.Summary)
	}
}

func TestMergeRepairIntoProposal_EmptyContentDoesNotOverwritePrior(t *testing.T) {
	prior := &ai.Result{
		Files: []ai.GeneratedFile{
			{Path: "pages/home.liquid", Action: "update", Content: "HOME_V1"},
			{Path: "components/store-hero-banner.liquid", Action: "update", Content: "HERO_V1"},
		},
	}
	repair := &ai.Result{
		Files: []ai.GeneratedFile{
			{Path: "components/store-hero-banner.liquid", Action: "update", Content: ""},
			{Path: "pages/css/home.css", Action: "create", Content: "CSS_NEW"},
		},
	}
	got := mergeRepairIntoProposal(prior, repair)
	hero, ok := fileByPath(got.Files, "components/store-hero-banner.liquid")
	if !ok || hero.Content != "HERO_V1" {
		t.Fatalf("empty repair must not blank prior hero, got %+v", hero)
	}
	if _, ok := fileByPath(got.Files, "pages/css/home.css"); !ok {
		t.Fatal("non-empty repair path should still append")
	}
}

func fullHomeProposal(badCSS string) *ai.Result {
	return &ai.Result{
		Summary: "regenerated homepage",
		Files: []ai.GeneratedFile{
			{Path: "pages/home.liquid", Action: "update", Content: goodPageContent},
			{Path: "pages/css/home.css", Action: "create", Content: badCSS},
			{Path: "components/store-hero-banner.liquid", Action: "update", Content: "<section class=\"hero\">{{ store.name }}</section>"},
			{Path: "components/css/store-hero-banner.css", Action: "create", Content: ".hero { display: block; }"},
			{Path: "js/store-hero-banner.js", Action: "update", Content: "window.hero = true;"},
			{Path: "components/testimonials.liquid", Action: "create", Content: "<section class=\"testimonials\">ok</section>"},
		},
		LayoutLinksToAdd:   []string{"pages/css/home.css", "components/css/store-hero-banner.css"},
		LayoutScriptsToAdd: []string{"js/store-hero-banner.js"},
		InputTokens:        100,
		OutputTokens:       50,
	}
}

func fullHomeSnapshot() themecheck.Snapshot {
	return themecheck.Snapshot{
		Paths: map[string]bool{
			"liquid/layout-start.liquid": true,
			"liquid/layout-end.liquid":   true,
		},
		Files: map[string]string{
			"defaults.json": `{"colors": {"primary": "#1e3a8a", "secondary": "#111111", "background": "#ffffff"}}`,
		},
	}
}

// TestCheckAndRepair_FullHomeCSSRepairPreservesAllFiles is the audit failure
// mode: first propose returns the complete homepage; themecheck theme-token
// repair returns only CSS; staged result must still contain every original file.
func TestCheckAndRepair_FullHomeCSSRepairPreservesAllFiles(t *testing.T) {
	// #cafe01 is not in defaults.json — AutoFixThemeTokens cannot map it,
	// so a real repair Generate round-trip is required.
	badCSS := `.hero-title { color: #cafe01; }`
	fixedCSS := `.hero-title { color: var(--theme-primary, #1e3a8a); }`

	first := fullHomeProposal(badCSS)
	repairOnlyCSS := &ai.Result{
		Summary:      "fixed theme tokens",
		Files:        []ai.GeneratedFile{{Path: "pages/css/home.css", Action: "create", Content: fixedCSS}},
		InputTokens:  20,
		OutputTokens: 10,
	}

	fg := &fakeGenerator{results: []*ai.Result{repairOnlyCSS}}
	svc := &Service{gen: fg}
	in := GenerateInput{
		TenantID:  1,
		ThemeSlug: "demo",
		Prompt:    "Regenerate the entire homepage from scratch as a premium software house website",
	}
	tc := ai.ThemeContext{PageCreatePrepared: true}

	got, _, err := svc.checkAndRepair(context.Background(), in, "chat-1", tc, nil, first, fullHomeSnapshot(), nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fg.calls != 1 {
		t.Fatalf("expected 1 repair Generate call, got %d", fg.calls)
	}
	wantPaths := []string{
		"pages/home.liquid",
		"pages/css/home.css",
		"components/store-hero-banner.liquid",
		"components/css/store-hero-banner.css",
		"js/store-hero-banner.js",
		"components/testimonials.liquid",
	}
	if len(got.Files) != len(wantPaths) {
		t.Fatalf("expected %d staged files, got %d (%v)", len(wantPaths), len(got.Files), proposalPaths(got))
	}
	for _, p := range wantPaths {
		if _, ok := fileByPath(got.Files, p); !ok {
			t.Errorf("staged draft missing %s (repair collapsed scope)", p)
		}
	}
	css, _ := fileByPath(got.Files, "pages/css/home.css")
	if css.Content != fixedCSS {
		t.Errorf("home.css not repaired: %q", css.Content)
	}
	home, _ := fileByPath(got.Files, "pages/home.liquid")
	if home.Content != goodPageContent {
		t.Error("pages/home.liquid content was altered unexpectedly")
	}
}

func TestCheckAndRepair_FullHomeEmptyRepairKeepsOriginal(t *testing.T) {
	badCSS := `.hero-title { color: #cafe01; }`
	fixedCSS := `.hero-title { color: var(--theme-primary, #1e3a8a); }`
	first := fullHomeProposal(badCSS)

	// First repair returns nothing useful; second returns the CSS fix.
	empty := &ai.Result{Summary: "still looking", Files: nil, InputTokens: 5, OutputTokens: 2}
	fixed := &ai.Result{
		Summary:      "fixed",
		Files:        []ai.GeneratedFile{{Path: "pages/css/home.css", Action: "create", Content: fixedCSS}},
		InputTokens:  10,
		OutputTokens: 8,
	}
	fg := &fakeGenerator{results: []*ai.Result{empty, fixed}}
	svc := &Service{gen: fg}
	in := GenerateInput{
		TenantID:  1,
		ThemeSlug: "demo",
		Prompt:    "Regenerate the entire homepage from scratch",
	}
	tc := ai.ThemeContext{PageCreatePrepared: true}

	got, _, err := svc.checkAndRepair(context.Background(), in, "chat-1", tc, nil, first, fullHomeSnapshot(), nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fg.calls != 2 {
		t.Fatalf("expected 2 repair calls, got %d", fg.calls)
	}
	if len(got.Files) != 6 {
		t.Fatalf("expected full homepage still staged, got %d files: %v", len(got.Files), proposalPaths(got))
	}
	if _, ok := fileByPath(got.Files, "pages/home.liquid"); !ok {
		t.Fatal("pages/home.liquid missing after empty repair round")
	}
}

// TestCheckAndRepair_NonHomeSingleFileRepairUnchanged covers case E: a normal
// single-file repair still replaces that file and does not invent extras.
func TestCheckAndRepair_NonHomeSingleFileRepairUnchanged(t *testing.T) {
	fg := &fakeGenerator{results: []*ai.Result{goodResult()}}
	svc := &Service{gen: fg}
	in := GenerateInput{TenantID: 1, ThemeSlug: "demo", Prompt: "fix the offers page field"}

	got, _, err := svc.checkAndRepair(context.Background(), in, "chat-1", ai.ThemeContext{}, nil, badResult(), testSnapshot(), nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Files) != 1 || got.Files[0].Path != "pages/offers.liquid" {
		t.Fatalf("expected single-file offers repair, got %v", proposalPaths(got))
	}
	if got.Files[0].Content != goodPageContent {
		t.Errorf("expected repaired content")
	}
}

func TestShouldPreserveProposalScope(t *testing.T) {
	if !shouldPreserveProposalScope(
		GenerateInput{Prompt: "Regenerate the entire homepage from scratch as a SaaS site"},
		ai.ThemeContext{},
	) {
		t.Error("full-home prompt should preserve scope")
	}
	if !shouldPreserveProposalScope(
		GenerateInput{Prompt: "tweak footer"},
		ai.ThemeContext{PageCreatePrepared: true},
	) {
		t.Error("PageCreatePrepared should preserve scope")
	}
	if shouldPreserveProposalScope(
		GenerateInput{Prompt: "make the offers button blue"},
		ai.ThemeContext{},
	) {
		t.Error("simple edit should not preserve full-home scope")
	}
}

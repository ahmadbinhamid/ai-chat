package themebuild

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"ai-chat/internal/ai"
	"ai-chat/internal/genfail"
	"ai-chat/internal/themefs"
)

// Reproduces the production failure: context-plan truncate mid-string +
// "\n…(truncated for simple-edit)" used as merge/staging base.
func TestResolveCanonicalPagesJSON_RejectsTruncatedPromptStub(t *testing.T) {
	valid := largePagesRegistry(40)
	truncated := truncateForSimpleEditPrompt(valid, 400)
	if !looksLikeTruncatedPagesJSON(truncated) {
		t.Fatal("fixture must look truncated")
	}
	if _, err := parsePagesJSONRaw(truncated); err == nil {
		t.Fatal("truncated stub must be invalid JSON (production failure mode)")
	}

	_, err := resolveCanonicalPagesJSON(truncated, "")
	if err == nil {
		t.Fatal("truncated stub must not become merge base")
	}
	if !strings.Contains(err.Error(), "truncated") && !strings.Contains(err.Error(), "current registry invalid") {
		t.Fatalf("want registry invalid wording: %v", err)
	}

	// Store wins over truncated ThemeContext hint.
	got, err := resolveCanonicalPagesJSON(valid, truncated)
	if err != nil {
		t.Fatal(err)
	}
	if got != valid {
		t.Fatal("store body must win")
	}
}

func TestCompoundCheckpoint_Create2Pages(t *testing.T) {
	base := largePagesRegistry(120)
	e1 := &themefs.PageEntry{
		Title: "SaaS Tips", Slug: "saas-tips", Page: "saas-tips",
		Type: "custom", Path: "/pages", Status: "published",
		SEOTitle: "JPRO SaaS Tips", SEODescription: "Keep me",
	}
	e2 := &themefs.PageEntry{
		Title: "DevOps Guide", Slug: "devops-guide", Page: "devops-guide",
		Type: "custom", Path: "/pages", Status: "published",
	}
	cp1, _, err := applyRegistryEntryCheckpoint(base, e1)
	if err != nil {
		t.Fatal(err)
	}
	cp2, _, err := applyRegistryEntryCheckpoint(cp1, e2)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateRegistryMergeInvariants(base, cp2, []string{"saas-tips", "devops-guide"}); err != nil {
		t.Fatal(err)
	}
	beforeRaws, _ := parsePagesJSONRaw(base)
	afterRaws, _ := parsePagesJSONRaw(cp2)
	if len(afterRaws) != len(beforeRaws)+2 {
		t.Fatalf("count %d → %d", len(beforeRaws), len(afterRaws))
	}
	for i := range beforeRaws {
		if string(beforeRaws[i]) != string(afterRaws[i]) {
			t.Fatalf("existing entry %d rewritten", i)
		}
	}
}

func TestCompoundCheckpoint_Create2ThenMenuKeepsRegistry(t *testing.T) {
	base := largePagesRegistry(90)
	e1 := &themefs.PageEntry{Slug: "one", Page: "one", Type: "custom", Status: "published", Path: "/pages"}
	e2 := &themefs.PageEntry{Slug: "two", Page: "two", Type: "custom", Status: "published", Path: "/pages"}
	cp, _, err := applyRegistryEntryCheckpoint(base, e1)
	if err != nil {
		t.Fatal(err)
	}
	cp, _, err = applyRegistryEntryCheckpoint(cp, e2)
	if err != nil {
		t.Fatal(err)
	}
	// Menu step must not mutate registry checkpoint.
	progress := CompoundProgress{
		Completed: []CompoundStep{
			{Label: "Create page 1 of 2", Kind: CompoundStepCreatePage},
			{Label: "Create page 2 of 2", Kind: CompoundStepCreatePage},
		},
		Failed:       &CompoundStep{Label: "Add pages to navigation", Kind: CompoundStepAddToMenu},
		RegistryJSON: cp,
		Registries:   []*themefs.PageEntry{e1, e2},
		Accum: &ai.Result{Files: []ai.GeneratedFile{
			{Path: "pages/one.liquid", Action: "create", Content: "a"},
			{Path: "pages/two.liquid", Action: "create", Content: "b"},
			{Path: "defaults.json", Action: "update", Content: `{"menu":{"items":[]}}`},
		}},
	}
	accum, _, _, err := compoundPartialOrErr(progress, nil, base, fmt.Errorf("invalid model proposal: add-to-menu incomplete"))
	if err == nil {
		t.Fatal("expected partial")
	}
	body := pagesJSONFromResult(accum)
	if body == "" {
		t.Fatal("checkpoint pages.json required after menu failure")
	}
	if err := validateRegistryMergeInvariants(base, body, []string{"one", "two"}); err != nil {
		t.Fatal(err)
	}
}

func TestMergePageRegistryEntries_RawNewlineInTitleEscaped(t *testing.T) {
	base := `[{"slug":"home","page":"home","type":"home","status":"published"}]`
	entry := &themefs.PageEntry{
		Title: "Line1\nLine2", Slug: "blog-post", Page: "blog-post",
		Type: "custom", Path: "/pages", Status: "published",
		SEODescription: "desc with \"quotes\" and \\ backslash",
	}
	after, added, err := mergePageRegistryEntries(base, []*themefs.PageEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 {
		t.Fatalf("added=%v", added)
	}
	if !json.Valid([]byte(strings.TrimSpace(after))) {
		t.Fatal("merged pages.json must be valid JSON")
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(after), &rows); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if r["page"] == "blog-post" {
			found = true
			if r["title"] != "Line1\nLine2" {
				t.Fatalf("title not round-tripped: %#v", r["title"])
			}
		}
	}
	if !found {
		t.Fatal("new page missing")
	}
	// Serializer must not leave a raw newline inside a JSON string literal.
	if strings.Contains(after, "\"Line1\nLine2\"") {
		t.Fatal("raw newline inside JSON string literal")
	}
	if !strings.Contains(after, `\n`) {
		t.Fatal("expected escaped \\n in serialized JSON")
	}
}

func TestMergePageRegistryEntries_RejectsTruncatedBase(t *testing.T) {
	valid := largePagesRegistry(30)
	truncated := truncateForSimpleEditPrompt(valid, 400)
	_, _, err := mergePageRegistryEntries(truncated, []*themefs.PageEntry{
		{Slug: "x", Page: "x", Type: "custom", Status: "published", Path: "/pages"},
	})
	if err == nil {
		t.Fatal("must reject truncated merge base")
	}
}

func TestUpsertPagesJSONWithRegistry_PreservesOnInvalidBase(t *testing.T) {
	bad := `[{"title":"unterminated`
	entry := &themefs.PageEntry{Slug: "x", Page: "x", Type: "custom", Status: "published", Path: "/pages"}
	got := upsertPagesJSONWithRegistry(bad, entry)
	if got != bad {
		t.Fatal("invalid base must remain unchanged (no silent wipe)")
	}
}

func TestInvalidAccumulatedDraft_RecoversFromCheckpoint(t *testing.T) {
	base := largePagesRegistry(50)
	e1 := &themefs.PageEntry{Slug: "one", Page: "one", Type: "custom", Status: "published", Path: "/pages"}
	checkpoint, _, err := applyRegistryEntryCheckpoint(base, e1)
	if err != nil {
		t.Fatal(err)
	}
	progress := CompoundProgress{
		Completed:    []CompoundStep{{Label: "Create page 1 of 2"}},
		Failed:       &CompoundStep{Label: "Create page 2 of 2"},
		RegistryJSON: checkpoint,
		Registries:   []*themefs.PageEntry{e1},
		Accum: &ai.Result{Files: []ai.GeneratedFile{
			{Path: "pages/one.liquid", Action: "create", Content: "a"},
			// Model left a garbage pages.json — must be stripped/replaced.
			{Path: "pages.json", Action: "update", Content: truncateForSimpleEditPrompt(base, 200)},
		}},
	}
	// Pass a truncated "base" to simulate the old bug; checkpoint must win.
	accum, _, _, err := compoundPartialOrErr(progress, nil, truncateForSimpleEditPrompt(base, 200), fmt.Errorf("step 2 failed"))
	if err == nil {
		t.Fatal("expected partial")
	}
	body := pagesJSONFromResult(accum)
	if !isValidPagesJSON(body) {
		t.Fatalf("recovered body invalid: %q", body[:min(80, len(body))])
	}
	if looksLikeTruncatedPagesJSON(body) {
		t.Fatal("truncated stub must not be staged")
	}
	if err := validateRegistryMergeInvariants(base, body, []string{"one"}); err != nil {
		t.Fatal(err)
	}
}

func TestStagingConsistency_UsesStoreNotTruncatedTC(t *testing.T) {
	storeBody := largePagesRegistry(60)
	truncated := truncateForSimpleEditPrompt(storeBody, 400)
	current, err := resolveCanonicalPagesJSON(storeBody, truncated)
	if err != nil {
		t.Fatal(err)
	}
	result := &ai.Result{
		Files: []ai.GeneratedFile{
			{Path: "pages/saas-tips.liquid", Action: "create", Content: "{% layout-start %}x{% layout-end %}"},
		},
		PageRegistryEntry: &themefs.PageEntry{
			Slug: "saas-tips", Page: "saas-tips", Type: "custom", Status: "published", Path: "/pages",
		},
	}
	merged, _, err := mergePageRegistryEntries(current, []*themefs.PageEntry{result.PageRegistryEntry})
	if err != nil {
		t.Fatal(err)
	}
	result.Files = append(result.Files, ai.GeneratedFile{Path: "pages.json", Action: "update", Content: merged})
	if err := validatePageFileRegistryConsistency(current, result, []string{"saas-tips"}); err != nil {
		t.Fatal(err)
	}
	// Truncated TC as current must fail closed (old production path).
	if err := validatePageFileRegistryConsistency(truncated, result, []string{"saas-tips"}); err == nil {
		t.Fatal("truncated current registry must fail consistency")
	}
}

func TestGenfail_PagesRegistryInvalid_GenericMessage(t *testing.T) {
	err := fmt.Errorf("stage theme changes: pages.json consistency: current registry invalid: parse pages.json: invalid character '\\n' in string literal")
	c := genfail.Classify(err)
	if c.Code != genfail.CodePagesRegistryInvalid {
		t.Fatalf("code=%s", c.Code)
	}
	low := strings.ToLower(c.Message)
	if strings.Contains(low, "pages.json") || strings.Contains(low, "parse") || strings.Contains(low, "\\n") {
		t.Fatalf("merchant message leaked internals: %q", c.Message)
	}
	san := ai.SanitizeError(err)
	if strings.Contains(strings.ToLower(san), "pages.json") {
		t.Fatalf("sanitize leaked: %q", san)
	}
}

func TestRemovePageRegistryIdentities_UsesSerializer(t *testing.T) {
	base := largePagesRegistry(25)
	after, err := removePageRegistryIdentities(base, []string{"page-0003"})
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(strings.TrimSpace(after))) {
		t.Fatal("delete path must leave valid JSON")
	}
	if strings.Contains(after, `"page-0003"`) {
		t.Fatal("removed id still present")
	}
	if !strings.Contains(after, `"home"`) {
		t.Fatal("unrelated entry lost")
	}
}

func TestDuplicatePageRegistration_Upserts(t *testing.T) {
	base := `[{"slug":"home","page":"home","type":"home","status":"published"},{"slug":"blog","page":"blog","type":"blog","status":"published","title":"Old"}]`
	cp, added, err := applyRegistryEntryCheckpoint(base, &themefs.PageEntry{
		Slug: "blog", Page: "blog", Type: "blog", Status: "published", Path: "/pages", Title: "New Title",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 0 {
		t.Fatalf("upsert must not report add: %v", added)
	}
	raws, _ := parsePagesJSONRaw(cp)
	if len(raws) != 2 {
		t.Fatalf("count=%d", len(raws))
	}
	if !strings.Contains(cp, "New Title") {
		t.Fatal("title not upserted")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

package themebuild

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ai-chat/internal/ai"
	"ai-chat/internal/themefs"
)

func largePagesRegistry(n int) string {
	entries := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		slug := fmt.Sprintf("page-%04d", i)
		if i == 0 {
			slug = "home"
		}
		entries = append(entries, map[string]any{
			"title":           "Title " + slug,
			"slug":            slug,
			"page":            slug,
			"path":            "/pages",
			"type":            map[bool]string{true: "home", false: "custom"}[i == 0],
			"status":          "published",
			"seo_title":       "SEO " + slug,
			"seo_description": "Desc for " + slug,
		})
	}
	raw, _ := json.MarshalIndent(entries, "", "  ")
	return string(raw) + "\n"
}

func TestMergePageRegistryEntries_PreservesLargeRegistryAdd1(t *testing.T) {
	before := largePagesRegistry(520)
	entry := &themefs.PageEntry{
		Title: "New One", Slug: "brand-new-one", Page: "brand-new-one",
		Type: "custom", Path: "/pages", Status: "published",
	}
	after, added, err := mergePageRegistryEntries(before, []*themefs.PageEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0] != "brand-new-one" {
		t.Fatalf("added=%v", added)
	}
	if err := validatePagesJSONNonDestructive(before, after, added, true); err != nil {
		t.Fatal(err)
	}
	beforeRaws, _ := parsePagesJSONRaw(before)
	afterRaws, _ := parsePagesJSONRaw(after)
	if len(afterRaws) != len(beforeRaws)+1 {
		t.Fatalf("count %d → %d", len(beforeRaws), len(afterRaws))
	}
	// Existing home entry bytes must be byte-identical (no rewrite churn).
	if string(beforeRaws[0]) != string(afterRaws[0]) {
		t.Fatalf("existing entry rewritten:\nbefore=%s\nafter=%s", beforeRaws[0], afterRaws[0])
	}
	if !strings.Contains(after, `"brand-new-one"`) {
		t.Fatal("new entry missing")
	}
}

func TestMergePageRegistryEntries_Add2PreservesExisting(t *testing.T) {
	before := largePagesRegistry(500)
	regs := []*themefs.PageEntry{
		{Slug: "saas-tips", Page: "saas-tips", Type: "custom", Status: "published", Path: "/pages"},
		{Slug: "devops-guide", Page: "devops-guide", Type: "custom", Status: "published", Path: "/pages"},
	}
	after, added, err := mergePageRegistryEntries(before, regs)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 2 {
		t.Fatalf("added=%v want 2", added)
	}
	if err := validatePagesJSONNonDestructive(before, after, added, true); err != nil {
		t.Fatal(err)
	}
	beforeRaws, _ := parsePagesJSONRaw(before)
	afterRaws, _ := parsePagesJSONRaw(after)
	if len(afterRaws) != len(beforeRaws)+2 {
		t.Fatalf("count %d → %d", len(beforeRaws), len(afterRaws))
	}
	for i := range beforeRaws {
		if string(beforeRaws[i]) != string(afterRaws[i]) {
			t.Fatalf("entry %d rewritten", i)
		}
	}
}

func TestMergePageRegistryEntries_DuplicatePrevented(t *testing.T) {
	before := `[{"slug":"home","page":"home","type":"home","status":"published"},{"slug":"about","page":"about","type":"custom","status":"published"}]`
	regs := []*themefs.PageEntry{
		{Slug: "about", Page: "about", Type: "custom", Status: "published", Path: "/pages", Title: "About Updated"},
	}
	after, added, err := mergePageRegistryEntries(before, regs)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 0 {
		t.Fatalf("duplicate must upsert not add: added=%v", added)
	}
	afterRaws, _ := parsePagesJSONRaw(after)
	if len(afterRaws) != 2 {
		t.Fatalf("count=%d want 2", len(afterRaws))
	}
	if !strings.Contains(after, "About Updated") {
		t.Fatal("upsert title missing")
	}
}

func TestValidatePagesJSONNonDestructive_RejectsDrop(t *testing.T) {
	before := largePagesRegistry(100)
	proposed := `[{"slug":"home","page":"home"},{"slug":"only-new","page":"only-new"}]`
	err := isDestructivePagesJSONProposal(before, proposed)
	if err == nil {
		t.Fatal("expected destructive rejection")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Fatalf("want missing wording: %v", err)
	}
}

func TestStripModelPagesJSON_AndRecoverSafeAdd(t *testing.T) {
	current := `[{"slug":"home","page":"home","type":"home","status":"published"}]`
	proposed := `[
  {"slug":"home","page":"home","type":"home","status":"published"},
  {"slug":"saas-tips","page":"saas-tips","type":"custom","status":"published","path":"/pages"}
]`
	result := &ai.Result{
		Files: []ai.GeneratedFile{
			{Path: "pages/saas-tips.liquid", Action: "create", Content: "x"},
			{Path: "pages.json", Action: "update", Content: proposed},
		},
	}
	cleaned, err := prepareCompoundCreateStep(result, current)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range cleaned.Files {
		if strings.EqualFold(f.Path, "pages.json") {
			t.Fatal("model pages.json must be stripped")
		}
	}
	if cleaned.PageRegistryEntry == nil || cleaned.PageRegistryEntry.Slug != "saas-tips" {
		t.Fatalf("expected recovered registry: %+v", cleaned.PageRegistryEntry)
	}
}

func TestStripModelPagesJSON_RejectsDestructive(t *testing.T) {
	current := largePagesRegistry(50)
	result := &ai.Result{
		Files: []ai.GeneratedFile{
			{Path: "pages/x.liquid", Action: "create", Content: "x"},
			{Path: "pages.json", Action: "update", Content: `[{"slug":"x","page":"x"}]`},
		},
	}
	_, err := prepareCompoundCreateStep(result, current)
	if err == nil {
		t.Fatal("expected rejection of destructive pages.json")
	}
	if !strings.Contains(err.Error(), "proposal/tool contract mismatch") {
		t.Fatalf("want contract mismatch: %v", err)
	}
}

func TestInjectMergedPagesJSON_CompoundTwoPages(t *testing.T) {
	current := largePagesRegistry(510)
	result := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "pages/one.liquid", Action: "create", Content: "a"},
		{Path: "pages/two.liquid", Action: "create", Content: "b"},
		// Model tried a truncated rewrite — must be replaced by safe merge.
		{Path: "pages.json", Action: "update", Content: `[{"slug":"one"}]`},
	}}
	regs := []*themefs.PageEntry{
		{Slug: "one", Page: "one", Type: "custom", Status: "published", Path: "/pages"},
		{Slug: "two", Page: "two", Type: "custom", Status: "published", Path: "/pages"},
	}
	result, _ = stripModelPagesJSON(result)
	if err := injectMergedPagesJSON(result, current, regs); err != nil {
		t.Fatal(err)
	}
	var body string
	for _, f := range result.Files {
		if f.Path == "pages.json" {
			body = f.Content
		}
	}
	if body == "" {
		t.Fatal("merged pages.json missing")
	}
	beforeRaws, _ := parsePagesJSONRaw(current)
	afterRaws, _ := parsePagesJSONRaw(body)
	if len(afterRaws) != len(beforeRaws)+2 {
		t.Fatalf("count %d → %d", len(beforeRaws), len(afterRaws))
	}
	// Diff size proxy: new content should be larger, not hundreds of lines smaller.
	if len(body) < len(current) {
		t.Fatalf("merged pages.json shrank (%d → %d) — destructive", len(current), len(body))
	}
}

func TestCompoundPartial_KeepsPage1RegistryMerge(t *testing.T) {
	base := largePagesRegistry(80)
	page1 := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "pages/one.liquid", Action: "create", Content: "a"},
		{Path: "pages.json", Action: "update", Content: `[{"slug":"one"}]`}, // destructive model body
	}, PageRegistryEntry: &themefs.PageEntry{Slug: "one", Page: "one", Type: "custom", Status: "published", Path: "/pages"}}
	progress := CompoundProgress{
		Completed:  []CompoundStep{{Label: "Create page 1 of 2", Kind: CompoundStepCreatePage}},
		Failed:     &CompoundStep{Label: "Create page 2 of 2"},
		Accum:      page1,
		Registries: []*themefs.PageEntry{page1.PageRegistryEntry},
	}
	accum, _, regs, err := compoundPartialOrErr(progress, nil, base, fmt.Errorf("step 2 failed"))
	if err == nil {
		t.Fatal("expected partial err")
	}
	var partial *errCompoundPartial
	if !errors.As(err, &partial) {
		t.Fatalf("want errCompoundPartial, got %T %v", err, err)
	}
	if accum == nil {
		t.Fatal("accum required")
	}
	var body string
	for _, f := range accum.Files {
		if f.Path == "pages.json" {
			body = f.Content
			if f.Content == `[{"slug":"one"}]` {
				t.Fatal("destructive model pages.json must not survive partial finalize")
			}
		}
	}
	if body == "" {
		t.Fatal("expected safe merged pages.json on checkpoint")
	}
	if err := validatePagesJSONNonDestructive(base, body, []string{"one"}, true); err != nil {
		t.Fatal(err)
	}
	if len(regs) != 1 {
		t.Fatalf("regs=%d", len(regs))
	}
}

func TestCompoundStepPrompt_DoesNotEmbedTruncatedPagesJSON(t *testing.T) {
	step := CompoundStep{ID: 1, Kind: CompoundStepCreatePage, Label: "Create page 1 of 2", FocusedPrompt: "x"}
	huge := largePagesRegistry(200)
	prompt := compoundStepPreparedPrompt(step, "create 2 blog pages", huge, nil)
	if strings.Contains(prompt, "```json") {
		t.Fatal("must not embed truncated pages.json body")
	}
	if !strings.Contains(prompt, "home") {
		t.Fatal("should list identities")
	}
	if strings.Contains(strings.ToLower(prompt), "keep every entry when updating") {
		t.Fatal("must not invite pages.json rewrite")
	}
}

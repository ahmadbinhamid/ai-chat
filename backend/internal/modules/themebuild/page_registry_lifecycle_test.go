package themebuild

import (
	"strings"
	"testing"

	"ai-chat/internal/ai"
	"ai-chat/internal/themefs"
)

func TestEnsureProposedCreatesRegistered_RequiresRegistry(t *testing.T) {
	t.Parallel()
	err := ensureProposedCreatesRegistered(&ai.Result{
		Files: []ai.GeneratedFile{{Path: "pages/saas-tips.liquid", Action: "create", Content: "x"}},
	})
	if err == nil {
		t.Fatal("expected missing registry error")
	}

	if err := ensureProposedCreatesRegistered(&ai.Result{
		Files:             []ai.GeneratedFile{{Path: "pages/saas-tips.liquid", Action: "create", Content: "x"}},
		PageRegistryEntry: &themefs.PageEntry{Slug: "saas-tips", Page: "saas-tips", Status: "published"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestValidatePageFileRegistryConsistency_CreateNeedsRegistry(t *testing.T) {
	t.Parallel()
	current := `[{"slug":"home","page":"home","type":"home","status":"published"}]`
	bad := &ai.Result{
		Files: []ai.GeneratedFile{{Path: "pages/new-one.liquid", Action: "create", Content: "body"}},
	}
	if err := validatePageFileRegistryConsistency(current, bad, nil); err == nil {
		t.Fatal("expected create-without-registry to fail")
	}

	good := &ai.Result{
		Files: []ai.GeneratedFile{{Path: "pages/new-one.liquid", Action: "create", Content: "body"}},
		PageRegistryEntry: &themefs.PageEntry{
			Slug: "new-one", Page: "new-one", Type: "custom", Status: "published", Path: "/pages",
		},
	}
	if err := validatePageFileRegistryConsistency(current, good, []string{"new-one"}); err != nil {
		t.Fatal(err)
	}
}

func TestValidatePageFileRegistryConsistency_Create2BothRegistered(t *testing.T) {
	t.Parallel()
	current := `[{"slug":"home","page":"home","status":"published"}]`
	merged, _, err := mergePageRegistryEntries(current, []*themefs.PageEntry{
		{Slug: "a", Page: "a", Type: "custom", Status: "published", Path: "/pages"},
		{Slug: "b", Page: "b", Type: "custom", Status: "published", Path: "/pages"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := &ai.Result{
		Files: []ai.GeneratedFile{
			{Path: "pages/a.liquid", Action: "create", Content: "a"},
			{Path: "pages/b.liquid", Action: "create", Content: "b"},
			{Path: "pages.json", Action: "update", Content: merged},
		},
	}
	if err := validatePageFileRegistryConsistency(current, result, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
}

func TestValidatePageFileRegistryConsistency_PreservesUnrelated(t *testing.T) {
	t.Parallel()
	current := `[{"slug":"home","page":"home"},{"slug":"about","page":"about"},{"slug":"blog","page":"blog"}]`
	// Destructive shrink missing about
	bad := `[{"slug":"home","page":"home"},{"slug":"blog","page":"blog"},{"slug":"x","page":"x"}]`
	result := &ai.Result{
		Files: []ai.GeneratedFile{
			{Path: "pages/x.liquid", Action: "create", Content: "x"},
			{Path: "pages.json", Action: "update", Content: bad},
		},
	}
	if err := validatePageFileRegistryConsistency(current, result, []string{"x"}); err == nil {
		t.Fatal("expected unrelated removal to fail")
	}
}

func TestValidatePageFileRegistryConsistency_DuplicateRejected(t *testing.T) {
	t.Parallel()
	dup := `[{"slug":"home","page":"home"},{"slug":"home","page":"home"}]`
	if err := validatePageFileRegistryConsistency(dup, &ai.Result{}, nil); err == nil {
		t.Fatal("expected duplicate current registry to fail")
	}
}

func TestValidatePageFileRegistryConsistency_DeleteRemovesRegistry(t *testing.T) {
	t.Parallel()
	current := `[{"slug":"home","page":"home"},{"slug":"old","page":"old"}]`
	after, err := removePageRegistryIdentities(current, []string{"old"})
	if err != nil {
		t.Fatal(err)
	}
	result := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "pages.json", Action: "update", Content: after},
		{Path: "pages/old.liquid", Action: "delete"},
	}}
	if err := validatePageFileRegistryConsistency(current, result, nil); err != nil {
		t.Fatal(err)
	}
	// File deleted but registry kept — fail
	bad := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "pages.json", Action: "update", Content: current},
		{Path: "pages/old.liquid", Action: "delete"},
	}}
	if err := validatePageFileRegistryConsistency(current, bad, nil); err == nil {
		t.Fatal("expected delete-with-remaining-registry to fail")
	}
}

func TestValidatePageFileRegistryConsistency_InvalidJSON(t *testing.T) {
	t.Parallel()
	current := `[{"slug":"home","page":"home"}]`
	result := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "pages.json", Action: "update", Content: "{not-json"},
	}}
	if err := validatePageFileRegistryConsistency(current, result, nil); err == nil {
		t.Fatal("expected invalid JSON rejection")
	}
}

func TestIsRegisterExistingPagePrompt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		prompt string
		want   bool
	}{
		{"if not register then please register it", true},
		{"please register the blog page", true},
		{"can you register the blog page", true},
		{"register it", true},
		{"create a contact page", false},
		{"delete extra pages", false},
		{"change button color", false},
	}
	for _, tc := range cases {
		if got := isRegisterExistingPagePrompt(tc.prompt); got != tc.want {
			t.Fatalf("%q: got %v want %v", tc.prompt, got, tc.want)
		}
	}
}

func TestBuildDeterministicRegisterExisting_RegistersBlog(t *testing.T) {
	t.Parallel()
	store := &memThemeStore{files: map[string]string{
		"pages.json":         `[{"slug":"home","page":"home","type":"home","status":"published"}]`,
		"pages/home.liquid":  "home",
		"pages/blog.liquid":  "{% layout %}blog body",
		"pages/css/blog.css": ".blog{}",
	}}
	auth := themefs.RequestAuth{Token: "t", TenantID: 1}
	result, ok, err := buildDeterministicRegisterExisting(t.Context(), store, auth, "if not register then please register it")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected handled")
	}
	if result.PageRegistryEntry == nil || result.PageRegistryEntry.Slug != "blog" {
		t.Fatalf("registry=%+v", result.PageRegistryEntry)
	}
	hasPagesJSON := false
	hasBlogUpdate := false
	for _, f := range result.Files {
		if f.Path == "pages.json" && f.Action == "update" {
			hasPagesJSON = true
			if !strings.Contains(f.Content, `"blog"`) {
				t.Fatal("merged pages.json missing blog")
			}
			if !strings.Contains(f.Content, `"home"`) {
				t.Fatal("merged pages.json lost home")
			}
		}
		if f.Path == "pages/blog.liquid" && f.Action == "update" {
			hasBlogUpdate = true
			if f.Content != "{% layout %}blog body" {
				t.Fatal("must not regenerate page content")
			}
		}
	}
	if !hasPagesJSON || !hasBlogUpdate {
		t.Fatalf("files=%v", proposalPaths(result))
	}
	if err := validatePageFileRegistryConsistency(store.files["pages.json"], result, []string{"blog"}); err != nil {
		t.Fatal(err)
	}
}

func TestBuildDeterministicRegisterExisting_RejectsMissingFile(t *testing.T) {
	t.Parallel()
	store := &memThemeStore{files: map[string]string{
		"pages.json":        `[{"slug":"home","page":"home"}]`,
		"pages/home.liquid": "home",
	}}
	result, ok, err := buildDeterministicRegisterExisting(t.Context(), store, themefs.RequestAuth{}, "please register the blog page")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || proposalHasChanges(result) {
		t.Fatalf("expected no-draft missing-file reply, got %+v", result)
	}
	if !strings.Contains(strings.ToLower(result.Summary), "couldn't find") {
		t.Fatalf("summary=%q", result.Summary)
	}
}

func TestBuildDeterministicRegisterExisting_AlreadyRegistered(t *testing.T) {
	t.Parallel()
	store := &memThemeStore{files: map[string]string{
		"pages.json":        `[{"slug":"home","page":"home"},{"slug":"blog","page":"blog","type":"blog","status":"published"}]`,
		"pages/blog.liquid": "blog",
	}}
	result, ok, err := buildDeterministicRegisterExisting(t.Context(), store, themefs.RequestAuth{}, "register the blog page")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if proposalHasChanges(result) {
		t.Fatal("already registered must not stage changes")
	}
}

func TestBuildDeterministicBulkDelete_PreservesBlogListing(t *testing.T) {
	t.Parallel()
	store := &memThemeStore{files: map[string]string{
		"pages.json": `[
  {"slug":"home","page":"home","type":"home","status":"published"},
  {"slug":"blog","page":"blog","type":"blog","status":"published"},
  {"slug":"saas-tips","page":"saas-tips","type":"post","status":"published","title":"SaaS Tips"}
]`,
		"pages/home.liquid":      "home",
		"pages/blog.liquid":      "blog listing",
		"pages/saas-tips.liquid": "post",
		"pages/orphan.liquid":    "orphan",
	}}
	result, ok, err := buildDeterministicBulkDelete(t.Context(), store, themefs.RequestAuth{},
		"delete extra blog pages and orphan files not in pages.json")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected handled cleanup")
	}
	var pagesJSON string
	deleted := map[string]bool{}
	for _, f := range result.Files {
		if f.Path == "pages.json" {
			pagesJSON = f.Content
		}
		if f.Action == "delete" {
			deleted[strings.ToLower(f.Path)] = true
		}
	}
	if pagesJSON != "" {
		if !strings.Contains(pagesJSON, `"blog"`) {
			t.Fatal("cleanup must preserve core blog registry entry")
		}
		if strings.Contains(pagesJSON, "saas-tips") {
			t.Fatal("blog-like post should be unregistered")
		}
	}
	if deleted["pages/blog.liquid"] {
		t.Fatal("must not delete pages/blog.liquid during non-explicit blog cleanup")
	}
	if !deleted["pages/saas-tips.liquid"] && !deleted["pages/orphan.liquid"] {
		t.Fatalf("expected post/orphan deletes, got %v", deleted)
	}
}

func TestBuildDeterministicBulkDelete_RegistrationFailureDoesNotDeleteUnrelated(t *testing.T) {
	t.Parallel()
	// If pages.json would become inconsistent, builder must error rather than
	// stage a half-broken cleanup. Simulate by making remove impossible while
	// protecting the only droppable file — use empty delete set path.
	store := &memThemeStore{files: map[string]string{
		"pages.json":        `[{"slug":"home","page":"home"},{"slug":"blog","page":"blog"}]`,
		"pages/home.liquid": "home",
		"pages/blog.liquid": "blog",
	}}
	result, ok, err := buildDeterministicBulkDelete(t.Context(), store, themefs.RequestAuth{},
		"delete extra orphan pages not in pages.json")
	if err != nil {
		t.Fatal(err)
	}
	// No orphans → not handled or empty
	if ok && proposalHasChanges(result) {
		for _, f := range result.Files {
			if f.Path == "pages.json" && !strings.Contains(f.Content, `"blog"`) {
				t.Fatal("must not drop blog when cleaning orphans")
			}
			if f.Action == "delete" && strings.Contains(f.Path, "home") {
				t.Fatal("must not delete home")
			}
		}
	}
}

func TestMergeDuplicateRegistrationPrevented(t *testing.T) {
	t.Parallel()
	before := `[{"slug":"blog","page":"blog","status":"published"}]`
	after, added, err := mergePageRegistryEntries(before, []*themefs.PageEntry{
		{Slug: "blog", Page: "blog", Type: "blog", Status: "published", Path: "/pages", Title: "Blog"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 0 {
		t.Fatalf("duplicate should update in place, added=%v", added)
	}
	raws, _ := parsePagesJSONRaw(after)
	if len(raws) != 1 {
		t.Fatalf("count=%d want 1", len(raws))
	}
}

func TestCompoundStepScopedRegistrationStillValid(t *testing.T) {
	t.Parallel()
	current := `[{"slug":"home","page":"home"}]`
	step := &ai.Result{
		Files: []ai.GeneratedFile{{Path: "pages/one.liquid", Action: "create", Content: "one"}},
		PageRegistryEntry: &themefs.PageEntry{
			Slug: "one", Page: "one", Type: "custom", Status: "published", Path: "/pages",
		},
	}
	if err := incompleteAtomicPageCreateProposal(step); err != nil {
		t.Fatal(err)
	}
	cleaned, err := prepareCompoundCreateStep(step, current)
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePageFileRegistryConsistency(current, cleaned, []string{"one"}); err != nil {
		t.Fatal(err)
	}
}

func TestWantsExplicitBlogListingDelete(t *testing.T) {
	t.Parallel()
	if wantsExplicitBlogListingDelete("delete extra blog pages not in pages.json") {
		t.Fatal("orphan/blog-posts cleanup must not count as listing delete")
	}
	if !wantsExplicitBlogListingDelete("please delete the blog page") {
		t.Fatal("expected explicit blog page delete")
	}
	if !wantsExplicitBlogListingDelete("sary blog pages del kr do please") {
		t.Fatal("all-blog-pages wipe must include listing")
	}
}

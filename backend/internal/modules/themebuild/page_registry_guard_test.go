package themebuild

import (
	"strings"
	"testing"

	"ai-chat/internal/ai"
	"ai-chat/internal/themefs"
)

func TestSlugFromPageFilePath(t *testing.T) {
	cases := []struct {
		path           string
		wantSlug       string
		wantAuthScoped bool
		wantOK         bool
	}{
		{"pages/pricing.liquid", "pricing", false, true},
		{"pages/auth/my-account.liquid", "my-account", true, true},
		{"components/header.liquid", "", false, false},
		{"pages/css/pricing.css", "", false, false},
		{"pages.json", "", false, false},
	}
	for _, c := range cases {
		slug, authScoped, ok := slugFromPageFilePath(c.path)
		if ok != c.wantOK || slug != c.wantSlug || authScoped != c.wantAuthScoped {
			t.Errorf("slugFromPageFilePath(%q) = (%q, %v, %v), want (%q, %v, %v)",
				c.path, slug, authScoped, ok, c.wantSlug, c.wantAuthScoped, c.wantOK)
		}
	}
}

// One new page file, no registry entry proposed at all.
func TestSynthesizeMissingPageRegistry_FillsSingleCreate(t *testing.T) {
	result := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "pages/pricing.liquid", Action: "create", Content: "..."},
	}}
	synthesizeMissingPageRegistry(result)

	if result.PageRegistryEntry == nil {
		t.Fatal("expected a synthesized registry entry")
	}
	e := result.PageRegistryEntry
	if e.Slug != "pricing" || e.Page != "pricing" || e.Path != "/pages" || e.Type != "custom" || e.Status != "published" {
		t.Errorf("unexpected synthesized entry: %+v", e)
	}
}

// Covers the pages/auth/* route-prefix case.
func TestSynthesizeMissingPageRegistry_AuthScoped(t *testing.T) {
	result := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "pages/auth/loyalty.liquid", Action: "create"},
	}}
	synthesizeMissingPageRegistry(result)

	if result.PageRegistryEntry == nil || result.PageRegistryEntry.Path != "/pages/auth" {
		t.Fatalf("expected an auth-scoped registry entry, got %+v", result.PageRegistryEntry)
	}
}

// Even an entry that looks wrong (mismatched slug) must be left alone.
func TestSynthesizeMissingPageRegistry_NeverOverwritesExisting(t *testing.T) {
	original := &themefs.PageEntry{Slug: "something-else", Page: "something-else"}
	result := &ai.Result{
		Files:             []ai.GeneratedFile{{Path: "pages/pricing.liquid", Action: "create"}},
		PageRegistryEntry: original,
	}
	synthesizeMissingPageRegistry(result)

	if result.PageRegistryEntry != original {
		t.Error("expected the model-supplied registry entry to be left untouched")
	}
}

// A compound multi-page create has no single obvious identity to guess at.
func TestSynthesizeMissingPageRegistry_SkipsMultiCreate(t *testing.T) {
	result := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "pages/pricing.liquid", Action: "create"},
		{Path: "pages/about.liquid", Action: "create"},
	}}
	synthesizeMissingPageRegistry(result)

	if result.PageRegistryEntry != nil {
		t.Error("expected no synthesized entry for a multi-page create")
	}
}

// A created component file must never be mistaken for a page identity.
func TestSynthesizeMissingPageRegistry_SkipsNonPageFiles(t *testing.T) {
	result := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "components/widget.liquid", Action: "create"},
	}}
	synthesizeMissingPageRegistry(result)

	if result.PageRegistryEntry != nil {
		t.Error("expected no synthesized entry for a non-page file")
	}
}

func TestIsExplicitPageDeletionRequest(t *testing.T) {
	cases := []struct {
		prompt string
		slug   string
		want   bool
	}{
		{"delete the blog page", "blog", true},
		{"please remove the blog listing", "blog", true},
		{"unregister the home page", "home", true},
		{"clean up the orphaned blog posts", "blog", false}, // no delete verb
		{"redesign the blog page", "blog", false},           // no delete verb
		{"delete the pricing page", "blog", false},          // wrong slug
	}
	for _, c := range cases {
		if got := isExplicitPageDeletionRequest(c.prompt, c.slug); got != c.want {
			t.Errorf("isExplicitPageDeletionRequest(%q, %q) = %v, want %v", c.prompt, c.slug, got, c.want)
		}
	}
}

func TestDroppedProtectedSlugs(t *testing.T) {
	before := `[{"slug":"blog","page":"blog","status":"published"},{"slug":"home","page":"home","status":"published"}]`
	after := `[{"slug":"home","page":"home","status":"published"}]`

	dropped := droppedProtectedSlugs(before, after)
	if len(dropped) != 1 || dropped[0] != "blog" {
		t.Errorf("expected [blog] dropped, got %v", dropped)
	}

	if dropped := droppedProtectedSlugs(before, before); len(dropped) != 0 {
		t.Errorf("expected nothing dropped when before==after, got %v", dropped)
	}
}

// A proposal deleting pages/blog.liquid as a side effect must have that action stripped.
func TestProtectPages_BlocksDeleteWithoutExplicitRequest(t *testing.T) {
	result := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "pages/blog.liquid", Action: "delete"},
		{Path: "pages/orphan-post-1.liquid", Action: "delete"},
	}}

	got, blocked := protectPages(result, "clean up orphaned blog post pages", `[{"slug":"blog","page":"blog"}]`)

	if len(blocked) != 1 || blocked[0] != "blog" {
		t.Fatalf("expected blog blocked, got %v", blocked)
	}
	if len(got.Files) != 1 || got.Files[0].Path != "pages/orphan-post-1.liquid" {
		t.Errorf("expected only the orphan post delete to survive, got %+v", got.Files)
	}
}

// A merchant who genuinely wants the blog page gone must still be able to delete it.
func TestProtectPages_AllowsExplicitDeletion(t *testing.T) {
	result := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "pages/blog.liquid", Action: "delete"},
	}}

	got, blocked := protectPages(result, "delete the blog page, we don't want one anymore", `[{"slug":"blog","page":"blog"}]`)

	if len(blocked) != 0 {
		t.Errorf("expected nothing blocked for an explicit deletion request, got %v", blocked)
	}
	if len(got.Files) != 1 || got.Files[0].Action != "delete" {
		t.Errorf("expected the explicit delete to survive, got %+v", got.Files)
	}
}

// Covers the second vector: a direct pages.json rewrite that silently drops a protected slug's row.
func TestProtectPages_RevertsPagesJSONRewriteThatDropsProtectedSlug(t *testing.T) {
	current := `[{"slug":"blog","page":"blog","status":"published"},{"slug":"pricing","page":"pricing","status":"published"}]`
	rewritten := `[{"slug":"pricing","page":"pricing","status":"published"}]` // blog silently dropped

	result := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "pages.json", Action: "update", Content: rewritten},
	}}

	got, blocked := protectPages(result, "add a new field to the pricing page entry", current)

	if len(blocked) != 1 || blocked[0] != "blog" {
		t.Fatalf("expected blog blocked, got %v", blocked)
	}
	if got.Files[0].Content != current {
		t.Errorf("expected pages.json content reverted to the pre-turn baseline, got %q", got.Files[0].Content)
	}
}

// A proposal touching neither a protected page nor pages.json must pass through unchanged.
func TestProtectPages_LeavesUnrelatedFilesAlone(t *testing.T) {
	result := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "pages/pricing.liquid", Action: "update", Content: "new content"},
	}}

	got, blocked := protectPages(result, "make the pricing page nicer", `[{"slug":"pricing","page":"pricing"}]`)

	if len(blocked) != 0 {
		t.Errorf("expected nothing blocked, got %v", blocked)
	}
	if len(got.Files) != 1 || got.Files[0].Content != "new content" {
		t.Errorf("expected the unrelated file untouched, got %+v", got.Files)
	}
}

func TestProtectedPagesNote(t *testing.T) {
	if got := protectedPagesNote("Done.", nil); got != "Done." {
		t.Errorf("expected summary unchanged when nothing blocked, got %q", got)
	}
	got := protectedPagesNote("Cleaned up 3 orphaned pages.", []string{"blog"})
	if !strings.Contains(got, "Blog") || !strings.Contains(got, "protected") {
		t.Errorf("expected a note mentioning the protected page, got %q", got)
	}
}

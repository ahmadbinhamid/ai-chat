package themecheck

import (
	"testing"

	"ai-chat/internal/themefs"
)

func TestCheckPageRoute_ValidCustomPage(t *testing.T) {
	p := Proposal{
		Files:             []ProposedFile{{Path: "pages/offers.liquid", Action: "create"}},
		PageRegistryEntry: &themefs.PageEntry{Page: "offers", Slug: "offers", Path: "/pages", Type: "custom"},
	}
	if got := checkPageRoute(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckPageRoute_ValidAuthSubdir(t *testing.T) {
	p := Proposal{
		Files:             []ProposedFile{{Path: "pages/auth/loyalty.liquid", Action: "create"}},
		PageRegistryEntry: &themefs.PageEntry{Page: "loyalty", Slug: "loyalty", Path: "/pages/auth", Type: "custom"},
	}
	if got := checkPageRoute(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckPageRoute_MissingRegistration(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Action: "create"}}}
	got := checkPageRoute(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for a page with no registry entry, got %+v", got)
	}
}

func TestCheckPageRoute_EntryDoesNotMatchFile(t *testing.T) {
	p := Proposal{
		Files:             []ProposedFile{{Path: "pages/offers.liquid", Action: "create"}},
		PageRegistryEntry: &themefs.PageEntry{Page: "deals", Slug: "deals", Path: "/pages", Type: "custom"},
	}
	got := checkPageRoute(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for a mismatched entry, got %+v", got)
	}
}

func TestCheckPageRoute_SlugMustEqualPageForCustom(t *testing.T) {
	p := Proposal{
		PageRegistryEntry: &themefs.PageEntry{Page: "offers", Slug: "deals", Path: "/pages", Type: "custom"},
	}
	got := checkPageRoute(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for slug != page, got %+v", got)
	}
}

func TestCheckPageRoute_UpsertSameSlugAllowed(t *testing.T) {
	// Homepage regenerate / SEO refresh re-sends page_registry_entry for an
	// existing route — must not fail as "slug already taken".
	p := Proposal{
		Files: []ProposedFile{{Path: "pages/offers.liquid", Action: "update"}},
		PageRegistryEntry: &themefs.PageEntry{
			Page: "offers", Slug: "offers", Path: "/pages", Type: "custom", Status: "published",
		},
	}
	snap := Snapshot{Files: map[string]string{"pages.json": `[{"slug":"offers","page":"offers","type":"custom"}]`}}
	if got := checkPageRoute(p, snap); len(got) != 0 {
		t.Fatalf("expected upsert of same page to pass, got %+v", got)
	}
}

func TestCheckPageRoute_HomeUpsertAllowed(t *testing.T) {
	p := Proposal{
		Files: []ProposedFile{{Path: "pages/home.liquid", Action: "update"}},
		PageRegistryEntry: &themefs.PageEntry{
			Page: "home", Slug: "home", Path: "/pages", Type: "home", Status: "published",
		},
	}
	snap := Snapshot{Files: map[string]string{
		"pages.json": `[{"slug":"home","page":"home","type":"home","status":"published"}]`,
	}}
	if got := checkPageRoute(p, snap); len(got) != 0 {
		t.Fatalf("expected homepage registry upsert to pass, got %+v", got)
	}
}

func TestCheckPageRoute_SlugTakenByDifferentPage(t *testing.T) {
	p := Proposal{
		PageRegistryEntry: &themefs.PageEntry{Page: "offers", Slug: "offers", Path: "/pages", Type: "custom"},
	}
	snap := Snapshot{Files: map[string]string{
		"pages.json": `[{"slug":"offers","page":"old-offers","type":"custom"}]`,
	}}
	got := checkPageRoute(p, snap)
	if len(got) != 1 {
		t.Fatalf("expected 1 finding when another page owns the slug, got %+v", got)
	}
}

func TestCheckPageRoute_DuplicateSystemTypeDifferentPage(t *testing.T) {
	p := Proposal{
		PageRegistryEntry: &themefs.PageEntry{Page: "home-2", Slug: "home-2", Path: "/pages", Type: "home"},
	}
	snap := Snapshot{Files: map[string]string{"pages.json": `[{"slug":"home","page":"home","type":"home"}]`}}
	got := checkPageRoute(p, snap)
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for a second system-type entry, got %+v", got)
	}
}

func TestCheckPageRoute_NoEntryNoPageFiles(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "components/header.liquid", Action: "update"}}}
	if got := checkPageRoute(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings when no page is being created, got %+v", got)
	}
}

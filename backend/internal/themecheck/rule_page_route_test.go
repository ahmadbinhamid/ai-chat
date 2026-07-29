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

func TestCheckPageRoute_SlugAlreadyTaken(t *testing.T) {
	p := Proposal{
		PageRegistryEntry: &themefs.PageEntry{Page: "offers", Slug: "offers", Path: "/pages", Type: "custom"},
	}
	snap := Snapshot{Files: map[string]string{"pages.json": `[{"slug":"offers","type":"custom"}]`}}
	got := checkPageRoute(p, snap)
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for an already-taken slug, got %+v", got)
	}
}

func TestCheckPageRoute_DuplicateSystemType(t *testing.T) {
	p := Proposal{
		PageRegistryEntry: &themefs.PageEntry{Page: "home", Slug: "home", Path: "/pages", Type: "home"},
	}
	snap := Snapshot{Files: map[string]string{"pages.json": `[{"slug":"home","type":"home"}]`}}
	got := checkPageRoute(p, snap)
	// slug-taken and duplicate-system-type both fire here since the fixture
	// reuses the same slug — that's realistic (a system route's slug is
	// already registered) and both findings are independently correct.
	if len(got) != 2 {
		t.Fatalf("expected 2 findings (slug taken + duplicate system type), got %+v", got)
	}
}

func TestCheckPageRoute_NoEntryNoPageFiles(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "components/header.liquid", Action: "update"}}}
	if got := checkPageRoute(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings when no page is being created, got %+v", got)
	}
}

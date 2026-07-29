package themebuild

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-chat/internal/ai"
	"ai-chat/internal/themefs"
)

// newTestStoreServer returns an httptest.Server standing in for
// flowpos-backend's theme-file API — every GET reports the file as not
// existing yet (404), which is the normal "new page" case buildWritePlan
// needs to read through cleanly.
func newTestStoreServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
}

func TestBuildWritePlan_PageRegistryEntryMatchesFile_TopLevelPages(t *testing.T) {
	ts := newTestStoreServer(t)
	defer ts.Close()

	svc := &Service{store: themefs.NewStore(ts.URL)}
	auth := themefs.RequestAuth{Token: "t", TenantID: 1}
	result := &ai.Result{
		Files: []ai.GeneratedFile{{Path: "pages/offers.liquid", Action: "create", Content: "..."}},
		PageRegistryEntry: &themefs.PageEntry{
			Title: "Offers", Slug: "offers", Path: "/pages", Type: "custom", Page: "offers", Status: "draft",
		},
	}

	plan, err := svc.buildWritePlan(context.Background(), auth, result)
	if err != nil {
		t.Fatalf("buildWritePlan returned an error: %v", err)
	}
	if len(plan.files) != 1 {
		t.Fatalf("expected 1 planned file, got %d", len(plan.files))
	}
	if plan.files[0].path != "pages/offers.liquid" {
		t.Fatalf("unexpected planned file path: %q", plan.files[0].path)
	}
	if plan.files[0].pageMeta == nil {
		t.Fatal("expected pageMeta to be attached to pages/offers.liquid, got nil")
	}
	if plan.files[0].pageMeta.Slug != "offers" || plan.files[0].pageMeta.Title != "Offers" {
		t.Errorf("unexpected pageMeta: %+v", plan.files[0].pageMeta)
	}
}

func TestBuildWritePlan_PageRegistryEntryMatchesFile_AuthSubdir(t *testing.T) {
	ts := newTestStoreServer(t)
	defer ts.Close()

	svc := &Service{store: themefs.NewStore(ts.URL)}
	auth := themefs.RequestAuth{Token: "t", TenantID: 1}
	result := &ai.Result{
		Files: []ai.GeneratedFile{{Path: "pages/auth/loyalty.liquid", Action: "create", Content: "..."}},
		PageRegistryEntry: &themefs.PageEntry{
			Title: "Loyalty", Slug: "loyalty", Path: "/pages/auth", Type: "custom", Page: "loyalty", Status: "draft",
		},
	}

	plan, err := svc.buildWritePlan(context.Background(), auth, result)
	if err != nil {
		t.Fatalf("buildWritePlan returned an error: %v", err)
	}
	if len(plan.files) != 1 || plan.files[0].pageMeta == nil {
		t.Fatalf("expected pageMeta attached to the one planned file, got %+v", plan.files)
	}
	if plan.files[0].path != "pages/auth/loyalty.liquid" {
		t.Errorf("unexpected planned file path: %q", plan.files[0].path)
	}
}

func TestBuildWritePlan_PageRegistryEntryWithNoMatchingFileErrors(t *testing.T) {
	ts := newTestStoreServer(t)
	defer ts.Close()

	svc := &Service{store: themefs.NewStore(ts.URL)}
	auth := themefs.RequestAuth{Token: "t", TenantID: 1}
	result := &ai.Result{
		Files: []ai.GeneratedFile{{Path: "pages/offers.liquid", Action: "create", Content: "..."}},
		PageRegistryEntry: &themefs.PageEntry{
			Title: "Deals", Slug: "deals", Path: "/pages", Type: "custom", Page: "deals", Status: "draft",
		},
	}

	if _, err := svc.buildWritePlan(context.Background(), auth, result); err == nil {
		t.Fatal("expected an error when the registry entry doesn't match any proposed file")
	}
}

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

	plan, err := svc.buildWritePlan(context.Background(), svc.store, auth, result)
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

	plan, err := svc.buildWritePlan(context.Background(), svc.store, auth, result)
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

	if _, err := svc.buildWritePlan(context.Background(), svc.store, auth, result); err == nil {
		t.Fatal("expected an error when the registry entry doesn't match any proposed file")
	}
}

// TestBuildWritePlan_DirectLayoutStartEditSkipsSplice is the regression
// test for the safety net that replaced validateProposal's old outright
// rejection of a direct layout-start.liquid/layout-end.liquid edit (see
// pathsafety.go/writeplan.go's own doc comments): a turn that edits
// liquid/layout-start.liquid directly via files[] AND also sets
// LayoutLinksToAdd in the same turn must NOT also get a computed
// plan.layoutStart splice — that would either duplicate the audit row for
// the same (message_id, file_path) or, worse, silently overwrite the
// direct edit with stale pre-edit content when commitWritePlan writes the
// splice after plan.files. The direct edit's own content must be exactly
// what was proposed, untouched by the splice logic.
func TestBuildWritePlan_DirectLayoutStartEditSkipsSplice(t *testing.T) {
	ts := newTestStoreServer(t)
	defer ts.Close()

	svc := &Service{store: themefs.NewStore(ts.URL)}
	auth := themefs.RequestAuth{Token: "t", TenantID: 1}
	const directContent = "<html><!-- the model's own direct edit --></html>"
	result := &ai.Result{
		Files:            []ai.GeneratedFile{{Path: pathLayoutStart, Action: "update", Content: directContent}},
		LayoutLinksToAdd: []string{"pages/css/offers.css"},
	}

	plan, err := svc.buildWritePlan(context.Background(), svc.store, auth, result)
	if err != nil {
		t.Fatalf("buildWritePlan returned an error: %v", err)
	}
	if plan.layoutStart != nil {
		t.Fatalf("expected the layout-start splice to be skipped in favor of the direct edit, got %+v", plan.layoutStart)
	}
	if len(plan.files) != 1 || plan.files[0].path != pathLayoutStart || plan.files[0].content != directContent {
		t.Fatalf("expected the direct edit to survive untouched in plan.files, got %+v", plan.files)
	}
}

// TestBuildWritePlan_DirectLayoutEndEditSkipsSplice is the layout-end.liquid
// counterpart to the layout-start test above.
func TestBuildWritePlan_DirectLayoutEndEditSkipsSplice(t *testing.T) {
	ts := newTestStoreServer(t)
	defer ts.Close()

	svc := &Service{store: themefs.NewStore(ts.URL)}
	auth := themefs.RequestAuth{Token: "t", TenantID: 1}
	const directContent = "<!-- the model's own direct edit --></html>"
	result := &ai.Result{
		Files:              []ai.GeneratedFile{{Path: pathLayoutEnd, Action: "update", Content: directContent}},
		LayoutScriptsToAdd: []string{"js/offers.js"},
	}

	plan, err := svc.buildWritePlan(context.Background(), svc.store, auth, result)
	if err != nil {
		t.Fatalf("buildWritePlan returned an error: %v", err)
	}
	if plan.layoutEnd != nil {
		t.Fatalf("expected the layout-end splice to be skipped in favor of the direct edit, got %+v", plan.layoutEnd)
	}
	if len(plan.files) != 1 || plan.files[0].path != pathLayoutEnd || plan.files[0].content != directContent {
		t.Fatalf("expected the direct edit to survive untouched in plan.files, got %+v", plan.files)
	}
}

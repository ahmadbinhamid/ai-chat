package themebuild

import (
	"context"
	"strings"
	"testing"

	"ai-chat/internal/themefs"
)

func TestTitleFromSlug(t *testing.T) {
	cases := []struct{ slug, want string }{
		{"pricing", "Pricing"},
		{"about-us", "About Us"},
		{"contact", "Contact"},
		{"terms-and-conditions", "Terms And Conditions"},
	}
	for _, c := range cases {
		if got := titleFromSlug(c.slug); got != c.want {
			t.Errorf("titleFromSlug(%q) = %q, want %q", c.slug, got, c.want)
		}
	}
}

func TestParsePageRegistryEntries(t *testing.T) {
	entries := parsePageRegistryEntries(`[{"slug":"pricing","page":"pricing","status":"published"}]`)
	if len(entries) != 1 || entries[0].Slug != "pricing" || entries[0].Status != "published" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	if got := parsePageRegistryEntries(""); got != nil {
		t.Errorf("expected nil for empty pages.json, got %+v", got)
	}
	if got := parsePageRegistryEntries("not json"); got != nil {
		t.Errorf("expected nil for malformed pages.json, got %+v", got)
	}
}

// TestTryRegisterExistingPage_RegistersUnregisteredFile is the core
// success path: a file that exists on disk but has no pages.json entry
// gets a synthetic Result with its content unchanged and a
// PageRegistryEntry attached, zero model calls.
func TestTryRegisterExistingPage_RegistersUnregisteredFile(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{
		"pages/pricing.liquid": "PRICING CONTENT",
		"pages.json":           `[]`,
	})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}
	store := svc.store

	result, ok := svc.tryRegisterExistingPage(context.Background(), store, testStoreAuth(), "pricing")
	if !ok {
		t.Fatal("expected the deterministic register path to handle this")
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected exactly 1 file, got %d", len(result.Files))
	}
	f := result.Files[0]
	if f.Path != "pages/pricing.liquid" || f.Action != "update" || f.Content != "PRICING CONTENT" {
		t.Errorf("unexpected file: %+v", f)
	}
	if result.PageRegistryEntry == nil {
		t.Fatal("expected a PageRegistryEntry")
	}
	entry := result.PageRegistryEntry
	if entry.Slug != "pricing" || entry.Page != "pricing" || entry.Path != "/pages" ||
		entry.Type != "custom" || entry.Status != "published" {
		t.Errorf("unexpected registry entry: %+v", entry)
	}
	if result.Files[0].Content == "" {
		t.Error("content must never be blanked out — this is a registration, not a content change")
	}
}

// TestTryRegisterExistingPage_AuthScopedFile covers the pages/auth/*
// route-prefix case.
func TestTryRegisterExistingPage_AuthScopedFile(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{
		"pages/auth/my-account.liquid": "ACCOUNT",
		"pages.json":                   `[]`,
	})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}

	result, ok := svc.tryRegisterExistingPage(context.Background(), svc.store, testStoreAuth(), "my account")
	if !ok {
		t.Fatal("expected a match")
	}
	if result.PageRegistryEntry.Path != "/pages/auth" {
		t.Errorf("expected route path /pages/auth, got %q", result.PageRegistryEntry.Path)
	}
	if result.Files[0].Path != "pages/auth/my-account.liquid" {
		t.Errorf("unexpected file path: %q", result.Files[0].Path)
	}
}

// TestTryRegisterExistingPage_AlreadyRegisteredIsIdempotent covers the
// idempotent no-op case — a second "register the pricing page" must not
// error or re-propose the file, just report it's already done.
func TestTryRegisterExistingPage_AlreadyRegisteredIsIdempotent(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{
		"pages/pricing.liquid": "PRICING CONTENT",
		"pages.json":           `[{"slug":"pricing","page":"pricing","status":"published"}]`,
	})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}

	result, ok := svc.tryRegisterExistingPage(context.Background(), svc.store, testStoreAuth(), "pricing")
	if !ok {
		t.Fatal("expected the already-registered case to still be handled deterministically")
	}
	if len(result.Files) != 0 || result.PageRegistryEntry != nil {
		t.Errorf("already-registered result must propose no changes, got %+v", result)
	}
	if !result.AnsweredQuestion {
		t.Error("expected AnsweredQuestion=true for a no-op reply")
	}
}

// TestTryRegisterExistingPage_NoMatchingFileFallsThrough covers the safe
// failure mode: a slug that doesn't correspond to any real file must defer
// to normal generation (which might mean "create a new page"), not invent
// a registration for nothing.
func TestTryRegisterExistingPage_NoMatchingFileFallsThrough(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{"pages.json": `[]`})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}

	_, ok := svc.tryRegisterExistingPage(context.Background(), svc.store, testStoreAuth(), "nonexistent")
	if ok {
		t.Error("expected no match when no file exists for the guessed slug")
	}
}

// TestTryDiagnoseExistingPage_AllStates covers the deterministic diagnose
// path's four distinguishable structural states.
func TestTryDiagnoseExistingPage_AllStates(t *testing.T) {
	cases := []struct {
		name       string
		files      map[string]string
		nameHint   string
		wantOK     bool
		wantSubstr string
	}{
		{
			name: "published and registered — no problem found",
			files: map[string]string{
				"pages/pricing.liquid": "x",
				"pages.json":           `[{"slug":"pricing","page":"pricing","status":"published"}]`,
			},
			nameHint: "pricing", wantOK: true, wantSubstr: "didn't find a structural problem",
		},
		{
			name: "registered but draft",
			files: map[string]string{
				"pages/pricing.liquid": "x",
				"pages.json":           `[{"slug":"pricing","page":"pricing","status":"draft"}]`,
			},
			nameHint: "pricing", wantOK: true, wantSubstr: "not \"published\"",
		},
		{
			name: "file exists but not registered",
			files: map[string]string{
				"pages/pricing.liquid": "x",
				"pages.json":           `[]`,
			},
			nameHint: "pricing", wantOK: true, wantSubstr: "isn't registered",
		},
		{
			name: "registered but file missing",
			files: map[string]string{
				"pages.json": `[{"slug":"pricing","page":"pricing","status":"published"}]`,
			},
			nameHint: "pricing", wantOK: true, wantSubstr: "couldn't find its file on disk",
		},
		{
			name:     "neither file nor registration — ambiguous, fall through",
			files:    map[string]string{"pages.json": `[]`},
			nameHint: "pricing", wantOK: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts := newFakeThemeServer(t, c.files)
			defer ts.Close()
			svc := &Service{store: themefs.NewStore(ts.URL)}

			result, ok := svc.tryDiagnoseExistingPage(context.Background(), svc.store, testStoreAuth(), c.nameHint)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v (result=%+v)", ok, c.wantOK, result)
			}
			if !c.wantOK {
				return
			}
			if len(result.Files) != 0 || result.PageRegistryEntry != nil {
				t.Errorf("diagnose must never propose file changes, got %+v", result)
			}
			if !result.AnsweredQuestion {
				t.Error("expected AnsweredQuestion=true")
			}
			if !strings.Contains(result.Summary, c.wantSubstr) {
				t.Errorf("summary %q does not contain %q", result.Summary, c.wantSubstr)
			}
		})
	}
}

// TestTryDeterministicPageOp_EndToEnd exercises the detection + execution
// pipeline together the way doGenerate actually calls it.
func TestTryDeterministicPageOp_EndToEnd(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{
		"pages/pricing.liquid": "PRICING CONTENT",
		"pages.json":           `[]`,
	})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}

	result, ok := svc.tryDeterministicPageOp(context.Background(), svc.store, testStoreAuth(), "register the pricing page")
	if !ok {
		t.Fatal("expected the register path to fire")
	}
	if result.PageRegistryEntry == nil || result.PageRegistryEntry.Slug != "pricing" {
		t.Errorf("unexpected result: %+v", result)
	}
}

// TestTryDeterministicPageOp_OrdinaryPromptFallsThrough is the negative
// control: a normal redesign/content request must never be intercepted.
func TestTryDeterministicPageOp_OrdinaryPromptFallsThrough(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{
		"pages/pricing.liquid": "PRICING CONTENT",
		"pages.json":           `[]`,
	})
	defer ts.Close()
	svc := &Service{store: themefs.NewStore(ts.URL)}

	for _, prompt := range []string{
		"redesign the pricing page with a modern look",
		"make the homepage hero section bigger",
		"the pricing page is not working, please redesign it",
		"create a new pricing page",
	} {
		if _, ok := svc.tryDeterministicPageOp(context.Background(), svc.store, testStoreAuth(), prompt); ok {
			t.Errorf("prompt %q: expected no deterministic match", prompt)
		}
	}
}

package themebuild

import (
	"strings"
	"testing"

	"ai-chat/internal/ai"
)

func TestClassifyIntent_PagesListIsThemeQuery(t *testing.T) {
	cases := []string{
		"list return kro please",
		"page.json file ko read kr lo or check kr k btao kitny pages hn us me",
		"bhi mjy sb pages ki list do kindly",
		"list all pages",
		"how many pages are in pages.json",
		"kitny pages hn",
		"pages ki list do",
		"read pages.json and show me the pages",
	}
	for _, p := range cases {
		if !isPagesListPrompt(p) {
			t.Fatalf("%q: expected isPagesListPrompt", p)
		}
		got := ClassifyIntent(p, "", false)
		if got != IntentThemeQuery {
			t.Fatalf("%q: want theme_query got %s", p, got)
		}
		if intentUsesSimpleEditOneShot(got) {
			t.Fatalf("%q: must not use simple_edit", p)
		}
	}
}

func TestIsPagesListPrompt_SliderHomeComplaintIsNotList(t *testing.T) {
	cases := []string{
		"i ask you slider images not load and not show on home page but you change then about page why i need home page slider images please",
		"slider images not show on home page",
		"home page slider images broken please fix",
		"images not load on homepage hero",
	}
	for _, p := range cases {
		if isPagesListPrompt(p) {
			t.Fatalf("%q must NOT be pages-list fast path", p)
		}
	}
}

func TestClassifyIntent_BlogDeleteNotPagesList(t *testing.T) {
	p := "sary blog pages del kr do please mjy blogs new writedown krwana blog k sab pages page.json se remove kr do"
	if isPagesListPrompt(p) {
		t.Fatal("delete prompt must NOT be pages-list")
	}
	if !isBulkPageDeletePrompt(p) {
		t.Fatal("expected bulk page delete")
	}
	got := ClassifyIntent(p, "", false)
	if got != IntentComplexPage {
		t.Fatalf("want complex_page got %s", got)
	}
}

func TestClassifyIntent_OrphanPageFilesDelete(t *testing.T) {
	p := "or extra pages jo pages.json me ni wo fiels b pages k folder se dek kr do"
	if isPagesListPrompt(p) {
		t.Fatal("must not list")
	}
	if !isBulkPageDeletePrompt(p) {
		t.Fatal("expected file delete intent")
	}
	if ClassifyIntent(p, "", false) != IntentComplexPage {
		t.Fatalf("got %s", ClassifyIntent(p, "", false))
	}
}

func TestNormalizeProposedDeletes_EmptyPageStubsBecomeDelete(t *testing.T) {
	prompt := "or extra pages jo pages.json me ni wo fiels b pages k folder se dek kr do"
	r := &ai.Result{Files: []ai.GeneratedFile{
		{Path: "pages/about-us.liquid", Action: "update", Content: ""},
		{Path: "pages/blog.liquid", Action: "update", Content: "  "},
		{Path: "pages/home.liquid", Action: "update", Content: "keep me"},
		{Path: "components/old-hero.liquid", Action: "update", Content: ""},
	}}
	normalizeProposedDeletes(r, prompt)
	if r.Files[0].Action != "delete" || r.Files[1].Action != "delete" || r.Files[3].Action != "delete" {
		t.Fatalf("empty stubs should become delete: %+v", r.Files)
	}
	if r.Files[2].Action != "update" {
		t.Fatalf("non-empty home must stay update, got %q", r.Files[2].Action)
	}
}

func TestFormatPagesRegistryTable(t *testing.T) {
	raw := `[
	  {"title":"Home","slug":"home","type":"home","page":"home","status":"published"},
	  {"title":"Privacy","slug":"privacy","type":"custom","page":"privacy","status":"published"},
	  {"title":"Draft","slug":"wip","type":"custom","page":"wip","status":"draft"}
	]`
	got := formatPagesRegistryTable(raw)
	if !strings.Contains(got, "| Title |") || !strings.Contains(got, "`home`") || !strings.Contains(got, "`privacy`") {
		t.Fatalf("table missing rows: %s", got)
	}
	if !strings.Contains(got, "**3** total") {
		t.Fatalf("expected count in %s", got)
	}
	if !strings.Contains(got, "pages/privacy.liquid") {
		t.Fatalf("expected template path in %s", got)
	}
}

func TestPromptNamedPageSlug(t *testing.T) {
	cases := map[string]string{
		"update the privacy page according to software company": "privacy",
		"fix PrivateCompany page content":                    "privacy",
		"redesign home page from scratch":                       "home",
		"update about us page":                                  "about-us",
		"change contact us copy":                                "contact-us",
		"make the header blue":                                  "",
	}
	for p, want := range cases {
		if got := promptNamedPageSlug(p); got != want {
			t.Fatalf("%q: got %q want %q", p, got, want)
		}
	}
}

func TestPromptScopesToNamedPage_PrivacyOnly(t *testing.T) {
	p := "update the privacy page according to software company please contnent abi b jpro ka arha mjy kisi b page py jpro ni chy"
	if promptNamedPageSlug(p) != "privacy" {
		t.Fatal("expected privacy")
	}
	if !promptScopesToNamedPage(p) {
		t.Fatal("expected scoped to named page")
	}
}

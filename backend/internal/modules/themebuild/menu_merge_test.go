package themebuild

import (
	"encoding/json"
	"strings"
	"testing"

	"ai-chat/internal/themefs"
)

func sampleDefaultsJSON() string {
	return `{
  "snippets_path": "liquid",
  "colors": {"primary": "#1e3a8a"},
  "menu": {
    "items": [
      {"id": "home", "label": "Home", "url": "/", "children": []},
      {"id": "shop", "label": "Shop", "url": "/shop", "children": []},
      {"id": "blog", "label": "Blog", "url": "/blog", "children": []}
    ]
  },
  "footer": {"copyright": "© Test"}
}
`
}

func TestMergeAddToMenu_PreservesExistingAndAppends(t *testing.T) {
	t.Parallel()
	base := sampleDefaultsJSON()
	op := AddToMenuOperation{
		PageIdentity: "choosing-numbing-cream",
		Label:        "Choosing Numbing Cream",
		URL:          "/choosing-numbing-cream",
		ItemID:       "choosing-numbing-cream",
	}
	out, added, err := mergeAddToMenuOperation(base, op)
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Fatal("expected item to be added")
	}
	if err := validateMenuMergeInvariants(base, out, op); err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &root); err != nil {
		t.Fatal(err)
	}
	// Unrelated keys preserved.
	if string(root["snippets_path"]) != `"liquid"` {
		t.Fatalf("snippets_path changed: %s", root["snippets_path"])
	}
	if !strings.Contains(string(root["footer"]), "© Test") {
		t.Fatalf("footer changed: %s", root["footer"])
	}
	items, err := menuItemsFromRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("items=%d want 4", len(items))
	}
	lastID, _, lastURL, lastLabel := menuItemIdentity(items[3])
	if lastID != "choosing-numbing-cream" || lastURL != "/choosing-numbing-cream" || lastLabel != "Choosing Numbing Cream" {
		t.Fatalf("last item = %s %s %s", lastID, lastLabel, lastURL)
	}
	// Existing identities preserved in order.
	for i, want := range []string{"home", "shop", "blog"} {
		id, _, _, _ := menuItemIdentity(items[i])
		if id != want {
			t.Fatalf("item[%d]=%q want %q", i, id, want)
		}
	}
}

func TestMergeAddToMenu_DuplicateNoOp(t *testing.T) {
	t.Parallel()
	base := sampleDefaultsJSON()
	op := AddToMenuOperation{PageIdentity: "blog", Label: "Blog", URL: "/blog", ItemID: "blog"}
	out, added, err := mergeAddToMenuOperation(base, op)
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Fatal("expected duplicate to be no-op")
	}
	if out != base {
		t.Fatal("duplicate merge must leave defaults.json unchanged")
	}
}

func TestMergeAddToMenu_RejectsTruncatedStub(t *testing.T) {
	t.Parallel()
	stub := `{"menu":{"items":[{"id":"home","label":"Home…(truncated for simple-edit)`
	_, _, err := mergeAddToMenuOperation(stub, AddToMenuOperation{PageIdentity: "x"})
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("want truncated stub rejection, got %v", err)
	}
}

func TestResolveCanonicalDefaultsJSON_RejectsTruncated(t *testing.T) {
	t.Parallel()
	_, err := resolveCanonicalDefaultsJSON(`{"menu":{"items":[]}} …(truncated)`, "")
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("got %v", err)
	}
}

func TestApplyAddToMenuCheckpoint_TwoPages(t *testing.T) {
	t.Parallel()
	cp := sampleDefaultsJSON()
	regs := []*themefs.PageEntry{
		{Page: "page-one",Slug: "page-one", Title: "Page One", Type: "custom", Path: "/pages", Status: "published"},
		{Page: "page-two", Slug: "page-two", Title: "Page Two", Type: "custom", Path: "/pages", Status: "published"},
	}
	out, summary, err := runDeterministicAddToMenu(cp, regs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "Page One") || !strings.Contains(summary, "Page Two") {
		t.Fatalf("summary=%q", summary)
	}
	root, _, err := parseDefaultsRoot(out)
	if err != nil {
		t.Fatal(err)
	}
	items, err := menuItemsFromRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 5 {
		t.Fatalf("items=%d want 5", len(items))
	}
	if !menuItemAlreadyPresent(items, AddToMenuOperation{PageIdentity: "page-one", ItemID: "page-one", URL: "/page-one"}) {
		t.Fatal("page-one missing")
	}
	if !menuItemAlreadyPresent(items, AddToMenuOperation{PageIdentity: "page-two", ItemID: "page-two", URL: "/page-two"}) {
		t.Fatal("page-two missing")
	}
}

func TestValidateMenuMerge_RejectsUnrelatedKeyChange(t *testing.T) {
	t.Parallel()
	before := sampleDefaultsJSON()
	after := strings.Replace(before, `"primary": "#1e3a8a"`, `"primary": "#000000"`, 1)
	err := validateMenuMergeInvariants(before, after, AddToMenuOperation{PageIdentity: "blog", ItemID: "blog", URL: "/blog", Label: "Blog"})
	if err == nil || !strings.Contains(err.Error(), "unrelated key") {
		t.Fatalf("got %v", err)
	}
}

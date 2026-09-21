package themebuild

import (
	"context"
	"strings"
	"testing"

	"ai-chat/internal/modules/chat"
	"ai-chat/internal/themefs"
)

func TestWantsOrphanPageFileCleanup(t *testing.T) {
	p := "or extra pages jo pages.json me ni wo fiels b pages k folder se dek kr do"
	if !wantsOrphanPageFileCleanup(p) {
		t.Fatal("expected orphan cleanup")
	}
	if !isBulkPageDeletePrompt(p) {
		t.Fatal("expected bulk delete")
	}
}

func TestIsDeleteConfirmationPrompt(t *testing.T) {
	if !isDeleteConfirmationPrompt("ok do it please fast") {
		t.Fatal("expected confirmation")
	}
	if isDeleteConfirmationPrompt("change the header") {
		t.Fatal("must not treat edit as confirmation")
	}
}

func TestResolveBulkDeletePrompt_UsesPrior(t *testing.T) {
	prior := []chat.Message{
		{Role: chat.RoleUser, Content: "or extra pages jo pages.json me ni wo fiels b pages k folder se dek kr do"},
		{Role: chat.RoleAssistant, Content: "I will delete them"},
	}
	got := resolveBulkDeletePrompt("ok do it please fast", prior)
	if !isBulkPageDeletePrompt(got) {
		t.Fatalf("expected prior delete prompt, got %q", got)
	}
}

func TestBuildDeterministicBulkDelete_Orphans(t *testing.T) {
	store := &memThemeStore{files: map[string]string{
		"pages.json": `[
  {"title":"Home","slug":"home","type":"home","page":"home","path":"/","status":"published"},
  {"title":"About","slug":"about-us","type":"page","page":"about-us","path":"/pages","status":"published"}
]`,
		"pages/home.liquid":                           "home",
		"pages/about-us.liquid":                       "about",
		"pages/affiliates.liquid":                     "aff",
		"pages/best-numbing-cream-for-tattoos.liquid": "seo",
		"pages/blog.liquid":                           "blog",
		"pages/cart.liquid":                           "cart",
	}}
	prompt := "or extra pages jo pages.json me ni wo fiels b pages k folder se dek kr do"
	result, ok, err := buildDeterministicBulkDelete(context.Background(), store, themefs.RequestAuth{}, prompt)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected handled")
	}
	actions := map[string]string{}
	for _, f := range result.Files {
		actions[f.Path] = f.Action
	}
	for _, want := range []string{
		"pages/affiliates.liquid",
		"pages/best-numbing-cream-for-tattoos.liquid",
	} {
		if actions[want] != "delete" {
			t.Fatalf("expected delete %s, got %+v", want, actions)
		}
	}
	// Unregistered blog.liquid is still a core listing template — orphan
	// cleanup must not delete it (register it instead if missing from pages.json).
	if actions["pages/blog.liquid"] == "delete" {
		t.Fatalf("must not orphan-delete core blog listing: %+v", actions)
	}
	if actions["pages/home.liquid"] == "delete" || actions["pages/about-us.liquid"] == "delete" || actions["pages/cart.liquid"] == "delete" {
		t.Fatalf("must not delete registered/core pages: %+v", actions)
	}
	if actions["pages.json"] != "" {
		t.Fatalf("orphan-only cleanup must not rewrite pages.json, got %+v", actions)
	}
}

func TestBuildDeterministicBulkDelete_BlogRows(t *testing.T) {
	store := &memThemeStore{files: map[string]string{
		"pages.json": `[
  {"title":"Home","slug":"home","type":"home","page":"home","path":"/","status":"published"},
  {"title":"Blog","slug":"blog","type":"blog","page":"blog","path":"/pages","status":"published"},
  {"title":"My Blog Post","slug":"my-blog-post","type":"page","page":"my-blog-post","path":"/pages","status":"published"}
]`,
		"pages/home.liquid":         "home",
		"pages/blog.liquid":         "blog",
		"pages/my-blog-post.liquid": "post",
	}}
	prompt := "sary blog pages del kr do please mjy blogs page.json se remove kr do"
	result, ok, err := buildDeterministicBulkDelete(context.Background(), store, themefs.RequestAuth{}, prompt)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected handled")
	}
	var pagesJSON string
	deleted := map[string]bool{}
	for _, f := range result.Files {
		if f.Path == "pages.json" {
			pagesJSON = f.Content
			continue
		}
		if f.Action == "delete" {
			deleted[f.Path] = true
		}
	}
	if pagesJSON == "" {
		t.Fatal("expected pages.json update")
	}
	if strings.Contains(strings.ToLower(pagesJSON), "blog") {
		t.Fatalf("blog entries should be removed from pages.json: %s", pagesJSON)
	}
	if !deleted["pages/blog.liquid"] || !deleted["pages/my-blog-post.liquid"] {
		t.Fatalf("expected blog liquid deletes, got %v", deleted)
	}
	if deleted["pages/home.liquid"] {
		t.Fatal("must keep home")
	}
}

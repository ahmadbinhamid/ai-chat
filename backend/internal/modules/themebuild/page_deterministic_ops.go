package themebuild

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"ai-chat/internal/ai"
	"ai-chat/internal/pageintent"
	"ai-chat/internal/themefs"
)

// tryDeterministicPageOp answers register/diagnose-existing-page requests without calling the
// model, on a confirmed match; ok=false falls through to normal generation, never blocking a request.
func (s *Service) tryDeterministicPageOp(ctx context.Context, store themefs.ThemeStore, storeAuth themefs.RequestAuth, prompt string) (*ai.Result, bool) {
	if name, ok := pageintent.DetectRegisterExisting(prompt); ok {
		return s.tryRegisterExistingPage(ctx, store, storeAuth, name)
	}
	if name, ok := pageintent.DetectDiagnoseExisting(prompt); ok {
		return s.tryDiagnoseExistingPage(ctx, store, storeAuth, name)
	}
	return nil, false
}

// pageRegistryEntryLite is the subset of a pages.json record needed for a read-only lookup.
type pageRegistryEntryLite struct {
	Slug   string `json:"slug"`
	Page   string `json:"page"`
	Status string `json:"status"`
}

// parsePageRegistryEntries parses pages.json's flat JSON array; a malformed shape is treated as
// "no existing routes known."
func parsePageRegistryEntries(pagesJSON string) []pageRegistryEntryLite {
	if strings.TrimSpace(pagesJSON) == "" {
		return nil
	}
	var entries []pageRegistryEntryLite
	if err := json.Unmarshal([]byte(pagesJSON), &entries); err != nil {
		return nil
	}
	return entries
}

// findRegistryEntry checks both slug and page fields, covering a hand-edited entry where they've drifted.
func findRegistryEntry(entries []pageRegistryEntryLite, slug string) (pageRegistryEntryLite, bool) {
	for _, e := range entries {
		if e.Slug == slug || e.Page == slug {
			return e, true
		}
	}
	return pageRegistryEntryLite{}, false
}

// existingPageFile is what findExistingPageFile confirms on disk.
type existingPageFile struct {
	slug       string
	path       string
	authScoped bool
}

// findExistingPageFile checks via ListFiles, not ReadFile — ReadFile returns ("", nil) for a
// missing file, indistinguishable from an empty one.
func findExistingPageFile(ctx context.Context, store themefs.ThemeStore, storeAuth themefs.RequestAuth, nameHint string) (existingPageFile, bool, error) {
	slug := pageintent.Slugify(nameHint)
	if slug == "" {
		return existingPageFile{}, false, nil
	}
	tree, err := store.ListFiles(ctx, storeAuth)
	if err != nil {
		return existingPageFile{}, false, err
	}
	paths := make(map[string]bool)
	flattenFileTree(tree, paths)

	if p := "pages/" + slug + ".liquid"; paths[p] {
		return existingPageFile{slug: slug, path: p}, true, nil
	}
	if p := "pages/auth/" + slug + ".liquid"; paths[p] {
		return existingPageFile{slug: slug, path: p, authScoped: true}, true, nil
	}
	return existingPageFile{}, false, nil
}

// titleFromSlug renders a kebab-case slug as a display title — "about-us" -> "About Us".
func titleFromSlug(slug string) string {
	words := strings.Split(slug, "-")
	for i, w := range words {
		if w == "" {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// tryRegisterExistingPage confirms a real, not-yet-registered file exists before acting. On
// success the file content is unchanged; PageRegistryEntry rides along so Apply upserts pages.json normally.
func (s *Service) tryRegisterExistingPage(ctx context.Context, store themefs.ThemeStore, storeAuth themefs.RequestAuth, nameHint string) (*ai.Result, bool) {
	match, found, err := findExistingPageFile(ctx, store, storeAuth, nameHint)
	if err != nil {
		slog.Warn("deterministic register-existing-page: file lookup failed, falling back to normal generation",
			"name_hint", nameHint, "error", err)
		return nil, false
	}
	if !found {
		// No file matches: likely a new-page request, a different op this deterministic path doesn't attempt.
		return nil, false
	}

	pagesJSON, err := store.ReadFile(ctx, storeAuth, pathPagesJSON)
	if err != nil {
		slog.Warn("deterministic register-existing-page: pages.json read failed, falling back to normal generation",
			"slug", match.slug, "error", err)
		return nil, false
	}
	title := titleFromSlug(match.slug)
	if _, registered := findRegistryEntry(parsePageRegistryEntries(pagesJSON), match.slug); registered {
		return &ai.Result{
			Summary:          fmt.Sprintf("The %q page is already registered — nothing to do.", title),
			AnsweredQuestion: true,
		}, true
	}

	content, err := store.ReadFile(ctx, storeAuth, match.path)
	if err != nil {
		slog.Warn("deterministic register-existing-page: page file read failed, falling back to normal generation",
			"path", match.path, "error", err)
		return nil, false
	}

	routePath := "/pages"
	if match.authScoped {
		routePath = "/pages/auth"
	}

	return &ai.Result{
		Summary: fmt.Sprintf("Registered the %q page.", title),
		Files: []ai.GeneratedFile{{
			Path: match.path, Action: "update", Content: content,
		}},
		PageRegistryEntry: &themefs.PageEntry{
			Title: title,
			Slug:  match.slug,
			Page:  match.slug,
			Path:  routePath,
			Type:  "custom",
			// Draft status would leave the page 404ing in prod, defeating the point of registering it.
			Status: "published",
		},
	}, true
}

// tryDiagnoseExistingPage answers structural questions only (exists, registered, publish
// status) — never a content/template-logic diagnosis. It never proposes a file change.
func (s *Service) tryDiagnoseExistingPage(ctx context.Context, store themefs.ThemeStore, storeAuth themefs.RequestAuth, nameHint string) (*ai.Result, bool) {
	slug := pageintent.Slugify(nameHint)
	if slug == "" {
		return nil, false
	}

	tree, err := store.ListFiles(ctx, storeAuth)
	if err != nil {
		slog.Warn("deterministic diagnose-existing-page: ListFiles failed, falling back to normal generation",
			"slug", slug, "error", err)
		return nil, false
	}
	paths := make(map[string]bool)
	flattenFileTree(tree, paths)

	fileExists := paths["pages/"+slug+".liquid"]
	if !fileExists {
		fileExists = paths["pages/auth/"+slug+".liquid"]
	}

	pagesJSON, err := store.ReadFile(ctx, storeAuth, pathPagesJSON)
	if err != nil {
		slog.Warn("deterministic diagnose-existing-page: pages.json read failed, falling back to normal generation",
			"slug", slug, "error", err)
		return nil, false
	}
	entry, registered := findRegistryEntry(parsePageRegistryEntries(pagesJSON), slug)
	title := titleFromSlug(slug)

	var summary string
	switch {
	case !fileExists && !registered:
		// A wrong slug guess looks identical to a genuinely missing page; let normal generation grep for a closer match.
		return nil, false
	case !fileExists && registered:
		summary = fmt.Sprintf(
			"The %q page is registered in pages.json, but I couldn't find its file on disk (expected pages/%s.liquid) — that's very likely why it's not working. Ask me to recreate it and I will.",
			title, slug)
	case fileExists && !registered:
		summary = fmt.Sprintf(
			"The %q page's file exists, but it isn't registered in pages.json — it has no route yet, so it can't open. Ask me to register it and I'll add it.",
			title)
	case entry.Status != "published":
		status := entry.Status
		if status == "" {
			status = "draft (no status set)"
		}
		summary = fmt.Sprintf(
			"The %q page exists and is registered, but its status is %q, not \"published\" — an unpublished page returns a 404 on the live storefront (it's still visible in the dashboard preview). Ask me to publish it and I'll update its status.",
			title, status)
	default:
		summary = fmt.Sprintf(
			"The %q page exists, is registered, and is published — I didn't find a structural problem with its registration. If it's still not working, tell me more about what you're seeing (an error message, a blank page, missing content) and I'll take a closer look.",
			title)
	}

	return &ai.Result{Summary: summary, AnsweredQuestion: true}, true
}

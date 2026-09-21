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

// tryDeterministicPageOp checks prompt against pageintent's two narrow
// detectors — register an existing page, diagnose why an existing page
// isn't working — and, on a confirmed match, answers without ever calling
// the model. See pageintent's package doc comment for why each detector is
// deliberately tight: registering an existing page is a pages.json entry,
// which currently costs a full generation (a multi-minute, many-thousand-
// token model call) for what is a structured, zero-ambiguity edit once a
// matching file is confirmed on disk.
//
// ok is false whenever nothing here applies OR a detector matched but this
// function couldn't confirm enough to act safely (an I/O error, no file on
// disk matching the guessed slug, an ambiguous "register the page" with no
// name) — doGenerate falls through to normal generation in every ok=false
// case, exactly as if this function didn't exist. This is a fast path, not
// a correctness requirement: it must never be the reason a real request
// goes unanswered.
func (s *Service) tryDeterministicPageOp(ctx context.Context, store themefs.ThemeStore, storeAuth themefs.RequestAuth, prompt string) (*ai.Result, bool) {
	if name, ok := pageintent.DetectRegisterExisting(prompt); ok {
		return s.tryRegisterExistingPage(ctx, store, storeAuth, name)
	}
	if name, ok := pageintent.DetectDiagnoseExisting(prompt); ok {
		return s.tryDiagnoseExistingPage(ctx, store, storeAuth, name)
	}
	return nil, false
}

// pageRegistryEntryLite is the subset of a pages.json record the
// deterministic ops need to check registration/status — deliberately its
// own small type here rather than reusing themecheck.existingPageEntry
// (unexported, and this package must not depend on themecheck's internals
// for something this narrow) or themefs.PageEntry (all 14 fields, most
// unused for a read-only lookup).
type pageRegistryEntryLite struct {
	Slug   string `json:"slug"`
	Page   string `json:"page"`
	Status string `json:"status"`
}

// parsePageRegistryEntries parses the theme's current pages.json (a flat
// JSON array — see themefs.PageEntry's own doc comment on why this service
// no longer merges it locally). A malformed or unexpected shape is treated
// as "no existing routes known," matching themecheck's rule_page_route.go
// own parseExistingPages behavior for the same file.
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

// findRegistryEntry looks up slug by either its slug or page identity field
// — spec §5's "page" basename and "slug" are the same value for a custom
// page, but checking both is cheap and covers a hand-edited or historical
// entry where they've drifted.
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

// findExistingPageFile confirms a candidate page file actually exists in
// the theme's file tree — via ListFiles, not ReadFile: Store.ReadFile
// returns ("", nil) for a missing file (see its own doc comment), which is
// indistinguishable from a real but empty file by error alone, so ListFiles
// membership is the only reliable existence check here.
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

// titleFromSlug renders a kebab-case slug as a display title —
// "about-us" -> "About Us". Only used to fill PageEntry.Title/the reply
// summary when registering a page the merchant didn't otherwise supply a
// title for; FlowPOS/the merchant can always rename it afterward.
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

// tryRegisterExistingPage implements the confirmed half of
// tryDeterministicPageOp's register path: nameHint has already passed
// pageintent.DetectRegisterExisting's trigger/edit-cue checks, but that
// alone is never enough to act — a real file must exist, and it must not
// already be registered.
//
// On success, the returned *ai.Result carries an "update" action on the
// existing file with its content byte-for-byte UNCHANGED, plus a
// PageRegistryEntry — buildWritePlan (unmodified, see writeplan.go)
// attaches that entry as PageMeta on the matching file, and
// flowpos-backend's own ThemeFileService.save upserts pages.json from it
// on Apply, exactly as it would for a model-authored proposal. This
// service never merges pages.json content itself (see themefs.PageEntry's
// doc comment) — reusing that existing path means this deterministic op
// needs no pages.json-writing code of its own.
func (s *Service) tryRegisterExistingPage(ctx context.Context, store themefs.ThemeStore, storeAuth themefs.RequestAuth, nameHint string) (*ai.Result, bool) {
	match, found, err := findExistingPageFile(ctx, store, storeAuth, nameHint)
	if err != nil {
		slog.Warn("deterministic register-existing-page: file lookup failed, falling back to normal generation",
			"name_hint", nameHint, "error", err)
		return nil, false
	}
	if !found {
		// No file matches the guessed slug — most likely the merchant
		// actually wants a NEW page (create + register), which is a
		// different operation this deterministic op deliberately doesn't
		// attempt (see its own doc comment). Let normal generation decide.
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
			// Registering an existing page is the merchant explicitly
			// asking for it to become reachable — an omitted/draft status
			// would leave it 404ing in prod (spec §5), silently defeating
			// the entire point of asking to register it.
			Status: "published",
		},
	}, true
}

// tryDiagnoseExistingPage implements the confirmed half of
// tryDeterministicPageOp's diagnose path: nameHint has already passed
// pageintent.DetectDiagnoseExisting's trigger/edit-cue checks. This only
// answers the structural questions this service can check without the
// model — file exists, registered, publish status — never a content/
// template-logic diagnosis (that's genuinely out of scope: see the
// contract this mirrors, which explicitly does not implement
// fix_existing_page). The result never proposes a file change; it only
// replies.
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
		// Nothing on disk or in the registry matches the guessed slug at
		// all — this is exactly the case a wrong guess (a synonym, a typo,
		// the merchant's own informal name for the page) looks identical
		// to a genuinely missing page. Reporting "it doesn't exist" here
		// risks being flatly wrong; normal generation can grep_theme for a
		// closer match instead of this deterministic op guessing.
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

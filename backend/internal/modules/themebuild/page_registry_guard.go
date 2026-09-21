package themebuild

import (
	"regexp"
	"strings"

	"ai-chat/internal/ai"
	"ai-chat/internal/themefs"
)

// synthesizeMissingPageRegistry fills in an obviously-missing
// page_registry_entry when a proposal creates exactly one new
// pages/*.liquid file and doesn't register it. themecheck's page-route
// rule (rule_page_route.go) already rejects a create with no matching
// registry entry as an error — correct, but that error forces a full paid
// repair generation for what is almost always a simple omission, not a
// genuine ambiguity requiring the model's judgment. This fills the gap
// deterministically instead, before that repair round-trip is ever needed.
//
// Deliberately narrow: only fires for exactly one created page file (a
// compound multi-page create has no single obvious identity to guess at,
// and guessing wrong there is worse than paying for one repair round-trip
// — themecheck's own multi-create-needs-multiple-entries check still
// applies unmodified), and never overwrites an entry the model DID
// propose, even a wrong one — this only fills a true gap, never second-
// guesses a real answer.
func synthesizeMissingPageRegistry(result *ai.Result) {
	if result == nil || result.PageRegistryEntry != nil {
		return
	}

	var created *ai.GeneratedFile
	createdCount := 0
	for i := range result.Files {
		f := &result.Files[i]
		if f.Action != "create" {
			continue
		}
		if _, _, ok := slugFromPageFilePath(f.Path); ok {
			createdCount++
			created = f
		}
	}
	if createdCount != 1 || created == nil {
		return
	}

	slug, authScoped, _ := slugFromPageFilePath(created.Path)
	routePath := "/pages"
	if authScoped {
		routePath = "/pages/auth"
	}
	result.PageRegistryEntry = &themefs.PageEntry{
		Title: titleFromSlug(slug),
		Slug:  slug,
		Page:  slug,
		Path:  routePath,
		Type:  "custom",
		// Same reasoning as tryRegisterExistingPage's own Status default:
		// a page the merchant just had created is presumptively meant to
		// be reachable, not silently left in draft/404.
		Status: "published",
	}
}

// slugFromPageFilePath derives a page identity from a proposed file path —
// the inverse of tryRegisterExistingPage's own path construction — only for
// paths matching the exact pages/<slug>.liquid or pages/auth/<slug>.liquid
// shape spec §2 requires; anything else (a component, a CSS file) isn't a
// page and ok is false.
func slugFromPageFilePath(path string) (slug string, authScoped bool, ok bool) {
	switch {
	case strings.HasPrefix(path, "pages/auth/") && strings.HasSuffix(path, ".liquid"):
		return strings.TrimSuffix(strings.TrimPrefix(path, "pages/auth/"), ".liquid"), true, true
	case strings.HasPrefix(path, "pages/") && strings.HasSuffix(path, ".liquid"):
		return strings.TrimSuffix(strings.TrimPrefix(path, "pages/"), ".liquid"), false, true
	default:
		return "", false, false
	}
}

// protectedPageSlugs are page identities this service refuses to silently
// delete or unregister, regardless of what prompt produced a proposal that
// would do it — the merchant must ask for that specific page's removal
// explicitly (see isExplicitPageDeletionRequest).
//
//   - "blog": the canonical listing page. A generation working on
//     something unrelated (cleaning up orphaned blog post pages,
//     redesigning the homepage, anything touching pages.json) has
//     repeatedly been observed treating the listing itself as disposable
//     collateral — the same class of bug themecheck's
//     DowngradePreExistingFindings protects against for pre-existing
//     content inside a file (see preexisting.go), just for a whole page
//     instead of a snippet.
//   - "home": the storefront root. Losing its file or registration is the
//     single most disruptive accidental deletion this service could make
//     — every other page is reachable by name; home is what "/" resolves
//     to, with nothing to fall back on.
//
// Deliberately a small, explicit, named set — not "every system page type"
// (systemPageTypes in themecheck already prevents a SECOND registration of
// those; this protects an EXISTING one from removal, a different concern)
// and not ordinary content pages, which a merchant legitimately does
// delete routinely.
var protectedPageSlugs = map[string]bool{
	"blog": true,
	"home": true,
}

// explicitPageDeletionRe requires an unambiguous delete/remove/unregister
// verb — matched against the merchant's own prompt before a protected
// page's removal is ever allowed through.
var explicitPageDeletionRe = regexp.MustCompile(`(?i)\b(delete|remove|unregister|unpublish|take down|get rid of)\b`)

// isExplicitPageDeletionRequest reports whether prompt explicitly asks to
// remove slug's page, as opposed to some OTHER operation (an orphan
// cleanup, a homepage rebuild, a pages.json edit for an unrelated reason)
// that happens to also touch or replace that file/entry as a side effect.
//
// The slug/display-word match below is a plain substring check, looser
// than pageintent's own tight detectors — deliberately: getting this
// wrong the SAFE way (refusing a real deletion request, so the merchant
// has to be more explicit) only costs a clarification; getting it wrong
// the UNSAFE way (silently deleting "home" or "blog") is exactly what this
// guard exists to prevent, so it's biased hard toward the safe direction.
func isExplicitPageDeletionRequest(prompt, slug string) bool {
	if !explicitPageDeletionRe.MatchString(prompt) {
		return false
	}
	return strings.Contains(strings.ToLower(prompt), slug)
}

// protectPages guards two vectors, UNLESS prompt explicitly asked to
// remove that specific page (see isExplicitPageDeletionRequest):
//
//  1. A direct pages.json rewrite (result.Files has an "update" entry for
//     pages.json — a plain, always-valid file edit) that silently drops a
//     protected slug's row compared to currentPagesJSON, the theme's
//     pages.json content as read at the start of this turn (tc.PagesJSON).
//     This is the vector that's actually reachable today: the normal,
//     structured page_registry_entry path never needs this check at all,
//     since flowpos-backend's upsert there only ever touches the one row
//     being written (see themefs.PageEntry's own doc comment) and can't
//     drop an unrelated entry — only a model choosing to hand-edit
//     pages.json's full content directly can.
//  2. A delete action on a protectedPageSlugs file's own .liquid path.
//     Currently unreachable in practice: validateProposal (proposal.go)
//     only accepts Action "create"/"update" — "delete" has no path through
//     generateValidProposal today, so this branch is defense in depth for
//     if/when that ever changes (or for a caller that builds an *ai.Result
//     directly, bypassing validateProposal, as a test or a future
//     deterministic op might), not a gap this codebase currently has.
//
// Returns the (possibly modified) result and the slugs whose removal was
// blocked, for the caller to note in the reply so a silently-kept file
// isn't mistaken for the request having been carried out.
func protectPages(result *ai.Result, prompt, currentPagesJSON string) (*ai.Result, []string) {
	if result == nil || len(protectedPageSlugs) == 0 {
		return result, nil
	}

	var blocked []string
	kept := make([]ai.GeneratedFile, 0, len(result.Files))
	for _, f := range result.Files {
		if slug, ok := protectedSlugForPath(f.Path); ok && f.Action == "delete" {
			if isExplicitPageDeletionRequest(prompt, slug) {
				kept = append(kept, f)
				continue
			}
			blocked = append(blocked, slug)
			continue
		}
		if f.Path == pathPagesJSON && f.Action != "delete" {
			if dropped := droppedProtectedSlugs(currentPagesJSON, f.Content); len(dropped) > 0 {
				stillDropped := false
				for _, slug := range dropped {
					if isExplicitPageDeletionRequest(prompt, slug) {
						continue
					}
					stillDropped = true
					blocked = append(blocked, slug)
				}
				if stillDropped {
					// Keep the theme's current pages.json instead of the
					// model's rewrite — better to no-op this one file than
					// accept a registry that silently dropped a protected
					// entry the merchant never asked to remove.
					f.Content = currentPagesJSON
				}
			}
		}
		kept = append(kept, f)
	}
	result.Files = kept
	return result, blocked
}

// protectedSlugForPath reports whether path is a protected page's own
// liquid file.
func protectedSlugForPath(path string) (string, bool) {
	slug, _, ok := slugFromPageFilePath(path)
	if !ok || !protectedPageSlugs[slug] {
		return "", false
	}
	return slug, true
}

// droppedProtectedSlugs compares before/after pages.json content and
// reports which protected slugs were registered in before but are no
// longer present in after.
func droppedProtectedSlugs(before, after string) []string {
	beforeEntries := parsePageRegistryEntries(before)
	afterEntries := parsePageRegistryEntries(after)
	var dropped []string
	for slug := range protectedPageSlugs {
		if _, ok := findRegistryEntry(beforeEntries, slug); !ok {
			continue // wasn't registered before either — nothing to protect
		}
		if _, ok := findRegistryEntry(afterEntries, slug); !ok {
			dropped = append(dropped, slug)
		}
	}
	return dropped
}

// protectedPagesNote appends a short, explicit note to summary when
// protectPages blocked something — a silently-kept file must never look
// to the merchant like their request was simply carried out.
func protectedPagesNote(summary string, blocked []string) string {
	if len(blocked) == 0 {
		return summary
	}
	names := make([]string, len(blocked))
	for i, slug := range blocked {
		names[i] = titleFromSlug(slug)
	}
	note := "I kept the " + strings.Join(names, " and ") + " page as-is — it's protected from removal as a side effect of another change. Ask me explicitly to delete it if that's really what you want."
	if summary == "" {
		return note
	}
	return summary + "\n\n" + note
}

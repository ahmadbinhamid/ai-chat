package themebuild

import (
	"regexp"
	"strings"

	"ai-chat/internal/ai"
	"ai-chat/internal/themefs"
)

// synthesizeMissingPageRegistry fills a missing page_registry_entry when a proposal creates
// exactly one new pages/*.liquid file. Never overwrites an entry the model did propose.
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
		// A just-created page is presumptively meant to be reachable, not left in draft/404.
		Status: "published",
	}
}

// slugFromPageFilePath derives a page identity from a path matching pages/<slug>.liquid or
// pages/auth/<slug>.liquid; anything else returns ok=false.
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

// protectedPageSlugs are pages refused a silent delete/unregister — merchant must ask explicitly.
// "blog" is repeatedly seen deleted as unrelated collateral; "home" is the storefront root with no fallback.
var protectedPageSlugs = map[string]bool{
	"blog": true,
	"home": true,
}

// explicitPageDeletionRe requires an unambiguous delete/remove/unregister verb in the prompt
// before a protected page's removal is allowed through.
var explicitPageDeletionRe = regexp.MustCompile(`(?i)\b(delete|remove|unregister|unpublish|take down|get rid of)\b`)

// isExplicitPageDeletionRequest is a loose substring check, deliberately biased toward the safe
// direction: a false negative just costs a clarification, a false positive silently deletes "home" or "blog".
func isExplicitPageDeletionRequest(prompt, slug string) bool {
	if !explicitPageDeletionRe.MatchString(prompt) {
		return false
	}
	return strings.Contains(strings.ToLower(prompt), slug)
}

// protectPages guards two vectors unless prompt explicitly asked to remove that page: a
// pages.json rewrite dropping a protected slug's row, or a delete on its own .liquid file. Returns blocked slugs for the caller's reply.
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
					// Keep current pages.json rather than accept a registry that silently drops a protected entry.
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

// droppedProtectedSlugs reports which protected slugs were registered in before but not after.
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

// protectedPagesNote appends a note when protectPages blocked something, so a silently-kept
// file doesn't look like the request was carried out.
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

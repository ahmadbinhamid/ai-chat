package themebuild

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/themefs"
)

// Core storefront templates — never auto-deleted as "orphans" even if missing
// from pages.json (a registry bug must not wipe cart/home/auth).
// "blog" is the listing page: orphan/blog-post cleanup must not remove it
// unless the merchant explicitly deletes the blog listing itself.
var protectedOrphanBases = map[string]bool{
	"home": true, "cart": true, "product": true, "products": true,
	"shop": true, "category": true, "categories": true, "checkout": true,
	"search": true, "404": true, "collection": true, "collections": true,
	"wishlist": true, "account": true, "login": true, "register": true,
	"privacy": true, "terms": true, "cookie": true, "faq": true,
	"contact": true, "contact-us": true, "about": true, "about-us": true,
	"delivery-info": true, "return-refunds": true,
	"blog": true,
}


// deleteConfirmRe: short confirmations after the merchant already asked to
// delete ("ok do it please fast") — must NOT fall into simple_edit.
var deleteConfirmRe = regexp.MustCompile(`(?i)^(?:ok|okay|yes|yep|haan|han|ji|sure|do\s+it|kr\s*do|kar\s*do|please|jaldi|fast)(?:[\s,]+(?:ok|okay|yes|yep|haan|han|ji|sure|do\s+it|kr\s*do|kar\s*do|please|jaldi|fast|it))*[!?.]*$`)

func isDeleteConfirmationPrompt(prompt string) bool {
	p := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
	p = strings.TrimRight(p, "!?.,;: ")
	return deleteConfirmRe.MatchString(p)
}

// resolveBulkDeletePrompt returns the prompt that describes what to delete.
// Confirmations ("ok do it") reuse the prior user bulk-delete message.
func resolveBulkDeletePrompt(prompt string, prior []chat.Message) string {
	if isBulkPageDeletePrompt(prompt) {
		return prompt
	}
	if !isDeleteConfirmationPrompt(prompt) {
		return ""
	}
	for i := len(prior) - 1; i >= 0; i-- {
		if prior[i].Role != chat.RoleUser {
			continue
		}
		if isBulkPageDeletePrompt(prior[i].Content) {
			return prior[i].Content
		}
	}
	return ""
}

func wantsOrphanPageFileCleanup(prompt string) bool {
	p := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
	if strings.Contains(p, "extra") || strings.Contains(p, "orphan") ||
		strings.Contains(p, "folder") {
		return true
	}
	if strings.Contains(p, "pages.json me ni") || strings.Contains(p, "page.json me ni") ||
		strings.Contains(p, "not in pages") || strings.Contains(p, "aren't in pages") ||
		strings.Contains(p, "not registered") {
		return true
	}
	// Roman Urdu: files in pages/ that aren't listed in pages.json
	if (strings.Contains(p, "file") || strings.Contains(p, "fiels") || strings.Contains(p, "files")) &&
		strings.Contains(p, "pages") &&
		(strings.Contains(p, "ni") || strings.Contains(p, "not") || strings.Contains(p, "extra")) {
		return true
	}
	return false
}

func wantsBlogPageCleanup(prompt string) bool {
	return strings.Contains(strings.ToLower(prompt), "blog")
}

// wantsExplicitBlogListingDelete is true only when the merchant asks to remove
// the core blog listing page itself — not when cleaning orphan/extra blog posts.
func wantsExplicitBlogListingDelete(prompt string) bool {
	p := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
	if !pageMutateRe.MatchString(p) {
		return false
	}
	if strings.Contains(p, "all blog") || strings.Contains(p, "every blog") ||
		strings.Contains(p, "sary blog") || strings.Contains(p, "saare blog") ||
		strings.Contains(p, "sab blog") || strings.Contains(p, "sb blog") {
		return true
	}
	return regexp.MustCompile(`(?i)\b(?:delete|remove|drop|purge|erase|hata)\b[\s\S]{0,32}\b(?:the\s+)?blog\s*page\b`).MatchString(p) ||
		regexp.MustCompile(`(?i)\b(?:the\s+)?blog\s*page\b[\s\S]{0,24}\b(?:delete|remove|drop|hata)\b`).MatchString(p) ||
		strings.Contains(p, "delete blog listing") ||
		strings.Contains(p, "remove blog listing")
}

// isCanonicalBlogListingRow is the storefront blog index (page/slug == blog),
// not a one-off blog post page.
func isCanonicalBlogListingRow(r pagesJSONRow) bool {
	page := strings.ToLower(strings.TrimSpace(r.Page))
	slug := strings.ToLower(strings.TrimSpace(r.Slug))
	return page == "blog" || slug == "blog"
}

func wantsAffiliatesCleanup(prompt string) bool {
	return strings.Contains(strings.ToLower(prompt), "affiliate")
}


func isBlogLikeRow(r pagesJSONRow) bool {
	blob := strings.ToLower(strings.Join([]string{r.Title, r.Slug, r.Type, r.Page, r.Path}, " "))
	if strings.Contains(blob, "blog") {
		return true
	}
	typ := strings.ToLower(strings.TrimSpace(r.Type))
	return typ == "post" || typ == "article"
}

func isAffiliatesRow(r pagesJSONRow) bool {
	blob := strings.ToLower(strings.Join([]string{r.Title, r.Slug, r.Page}, " "))
	return strings.Contains(blob, "affiliate")
}

func rowLiquidPath(r pagesJSONRow) string {
	page := strings.TrimSpace(r.Page)
	if page == "" {
		page = strings.TrimSpace(r.Slug)
	}
	if page == "" {
		return ""
	}
	if strings.Contains(strings.ToLower(r.Path), "auth") {
		return "pages/auth/" + page + ".liquid"
	}
	return "pages/" + page + ".liquid"
}

func registeredLiquidPaths(rows []pagesJSONRow) map[string]bool {
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		if p := rowLiquidPath(r); p != "" {
			out[strings.ToLower(p)] = true
		}
	}
	return out
}

func orphanPageLiquidPaths(allPaths []string, registered map[string]bool) []string {
	var out []string
	for _, p := range allPaths {
		low := strings.ToLower(p)
		if !strings.HasPrefix(low, "pages/") || !strings.HasSuffix(low, ".liquid") {
			continue
		}
		if strings.HasPrefix(low, "pages/css/") || strings.HasPrefix(low, "pages/auth/") {
			continue
		}
		base := strings.TrimSuffix(path.Base(low), ".liquid")
		if protectedOrphanBases[base] {
			continue
		}
		if registered[low] {
			continue
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func companionCSSPath(liquidPath string) string {
	low := strings.ToLower(liquidPath)
	if !strings.HasPrefix(low, "pages/") || strings.HasPrefix(low, "pages/css/") || strings.HasPrefix(low, "pages/auth/") {
		return ""
	}
	base := strings.TrimSuffix(path.Base(liquidPath), path.Ext(liquidPath))
	return "pages/css/" + base + ".css"
}

// buildDeterministicBulkDelete builds a propose_changes-equivalent result
// without DeepSeek: pages.json updates + action=delete for orphan/blog files.
// ok=false means fall through to the model.
func buildDeterministicBulkDelete(
	ctx context.Context,
	store themefs.ThemeStore,
	auth themefs.RequestAuth,
	prompt string,
) (result *ai.Result, ok bool, err error) {
	if !isBulkPageDeletePrompt(prompt) {
		return nil, false, nil
	}
	orphan := wantsOrphanPageFileCleanup(prompt)
	blog := wantsBlogPageCleanup(prompt)
	affiliates := wantsAffiliatesCleanup(prompt)
	if !orphan && !blog && !affiliates {
		return nil, false, nil
	}

	tree, err := store.ListFiles(ctx, auth)
	if err != nil {
		return nil, false, err
	}
	allPaths := flattenThemePaths(tree)
	onDisk := make(map[string]bool, len(allPaths))
	for _, p := range allPaths {
		onDisk[p] = true
		onDisk[strings.ToLower(p)] = true
	}

	pagesRaw, err := store.ReadFile(ctx, auth, pathPagesJSON)
	if err != nil {
		return nil, false, err
	}
	rows, err := parsePagesJSONRows(pagesRaw)
	if err != nil {
		return nil, false, err
	}
	registered := registeredLiquidPaths(rows)

	deleteSet := map[string]bool{}
	addDelete := func(p string) {
		if p == "" {
			return
		}
		found := p
		if !onDisk[p] {
			found = ""
			low := strings.ToLower(p)
			for _, ap := range allPaths {
				if strings.ToLower(ap) == low {
					found = ap
					break
				}
			}
		}
		if found == "" {
			return
		}
		if rejectProtectedDelete(found) != nil {
			return
		}
		deleteSet[found] = true
		if css := companionCSSPath(found); css != "" {
			for _, ap := range allPaths {
				if strings.ToLower(ap) == strings.ToLower(css) {
					deleteSet[ap] = true
					break
				}
			}
		}
	}

	var keptRows []json.RawMessage
	var rawRows []json.RawMessage
	_ = json.Unmarshal([]byte(strings.TrimSpace(pagesRaw)), &rawRows)
	if len(rawRows) == 0 && len(rows) > 0 {
		// Wrapped form — rebuild from typed rows.
		for _, r := range rows {
			b, mErr := json.Marshal(r)
			if mErr != nil {
				return nil, false, mErr
			}
			rawRows = append(rawRows, b)
		}
	}

	pagesJSONChanged := false
	var removeIDs []string
	explicitBlogListing := wantsExplicitBlogListingDelete(prompt)
	if blog || affiliates {
		for i, rr := range rawRows {
			meta := pagesJSONRow{}
			if i < len(rows) {
				meta = rows[i]
			} else {
				_ = json.Unmarshal(rr, &meta)
			}
			drop := (blog && isBlogLikeRow(meta)) || (affiliates && isAffiliatesRow(meta))
			// Never unregister the core blog listing during "blog pages" /
			// orphan-style cleanup unless the merchant explicitly deletes it.
			if drop && blog && isCanonicalBlogListingRow(meta) && !explicitBlogListing {
				drop = false
			}
			if drop {
				pagesJSONChanged = true
				if id := pageRegistryIdentity(meta.Page, meta.Slug); id != "" {
					removeIDs = append(removeIDs, id)
				}
				if p := rowLiquidPath(meta); p != "" {
					addDelete(p)
				}
				continue
			}
			keptRows = append(keptRows, rr)
		}
		// Only delete pages/blog.liquid when the listing itself is targeted.
		if blog && explicitBlogListing {
			addDelete("pages/blog.liquid")
			removeIDs = append(removeIDs, "blog")
		}
	}

	if orphan {
		for _, p := range orphanPageLiquidPaths(allPaths, registered) {
			addDelete(p)
		}
	}

	if len(deleteSet) == 0 && !pagesJSONChanged {
		return nil, false, nil
	}

	// Consistency: never shrink pages.json for an identity whose liquid file
	// is still present and not staged for delete (avoids file-without-registry).
	originalPagesJSON := pagesRaw
	var mergedPagesJSON string
	if pagesJSONChanged {
		filteredRemove := make([]string, 0, len(removeIDs))
		seenRm := map[string]bool{}
		for _, id := range removeIDs {
			id = strings.TrimSpace(id)
			if id == "" || seenRm[id] {
				continue
			}
			seenRm[id] = true
			liquid := "pages/" + id + ".liquid"
			low := strings.ToLower(liquid)
			stagedDelete := false
			for p := range deleteSet {
				if strings.ToLower(p) == low {
					stagedDelete = true
					break
				}
			}
			fileAbsent := !onDisk[liquid] && !onDisk[low]
			if stagedDelete || fileAbsent {
				filteredRemove = append(filteredRemove, id)
			}
		}
		removeIDs = filteredRemove
		if len(removeIDs) == 0 {
			pagesJSONChanged = false
		} else {
			var mErr error
			mergedPagesJSON, mErr = removePageRegistryIdentities(originalPagesJSON, removeIDs)
			if mErr != nil {
				return nil, false, mErr
			}
			keptRows, _ = parsePagesJSONRaw(mergedPagesJSON)
		}
	}

	files := make([]ai.GeneratedFile, 0, len(deleteSet)+1)
	if pagesJSONChanged {
		files = append(files, ai.GeneratedFile{
			Path:    pathPagesJSON,
			Action:  "update",
			Content: mergedPagesJSON,
		})
	}

	deletePaths := make([]string, 0, len(deleteSet))
	for p := range deleteSet {
		deletePaths = append(deletePaths, p)
	}
	sort.Strings(deletePaths)
	for _, p := range deletePaths {
		files = append(files, ai.GeneratedFile{
			Path:    p,
			Action:  "delete",
			Content: "",
			Edits:   nil,
		})
	}

	summary := fmt.Sprintf(
		"Staged cleanup: %d file delete(s)",
		len(deletePaths),
	)
	if pagesJSONChanged {
		summary += fmt.Sprintf(" and updated `pages.json` (%d → %d entries)", len(rawRows), len(keptRows))
	}
	summary += ". Review the draft, then Apply to remove them from the theme."

	result = &ai.Result{
		Summary: summary,
		Files:   files,
	}
	if pagesJSONChanged {
		if err := validatePageFileRegistryConsistency(originalPagesJSON, result, nil); err != nil {
			return nil, false, err
		}
	}

	return result, true, nil
}

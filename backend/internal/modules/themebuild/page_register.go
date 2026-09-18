package themebuild

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"ai-chat/internal/ai"
	"ai-chat/internal/themefs"
)

// registerExistingRe matches pure "register existing page" asks — including
// "if not register then please register it". Must NOT fire on create-new-page.
var registerExistingRe = regexp.MustCompile(`(?i)(?:` +
	`\bif\s+not\s+register\b` +
	`|` +
	`\b(?:please\s+)?register\s+(?:it|them|this|the\s+page|the\s+blog|blog|page)\b` +
	`|` +
	`\bregister\b[\s\S]{0,40}\b(?:pages?\.json|registry|route)\b` +
	`|` +
	`\b(?:not\s+registered|isn'?t\s+registered|missing\s+(?:from\s+)?(?:pages?\.json|registry))\b[\s\S]{0,48}\bregister\b` +
	`|` +
	`\badd\b[\s\S]{0,24}\b(?:to\s+)?pages?\.json\b` +
	`)`)

func isRegisterExistingPagePrompt(prompt string) bool {
	p := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
	if p == "" {
		return false
	}
	if isBulkPageDeletePrompt(p) {
		return false
	}
	// New page creation (even "create and register") stays on the create path.
	if isMultiPageCreatePrompt(p) {
		return false
	}
	if pageCreateRe.MatchString(p) {
		return false
	}
	return registerExistingRe.MatchString(p)
}

func registerTargetSlugFromPrompt(prompt string) string {
	p := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
	// Prefer explicit named pages.
	if slug := promptNamedPageSlug(p); slug != "" {
		return slug
	}
	if strings.Contains(p, "blog") {
		return "blog"
	}
	return ""
}

// buildDeterministicRegisterExisting registers an on-disk page liquid that is
// missing from pages.json — no DeepSeek, no content regeneration.
// ok=false means fall through to the model.
func buildDeterministicRegisterExisting(
	ctx context.Context,
	store themefs.ThemeStore,
	auth themefs.RequestAuth,
	prompt string,
) (result *ai.Result, ok bool, err error) {
	if !isRegisterExistingPagePrompt(prompt) {
		return nil, false, nil
	}

	tree, err := store.ListFiles(ctx, auth)
	if err != nil {
		return nil, false, err
	}
	allPaths := flattenThemePaths(tree)
	onDisk := map[string]string{} // lower → actual path
	for _, p := range allPaths {
		onDisk[strings.ToLower(p)] = p
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
	registeredIDs := map[string]bool{}
	for _, r := range rows {
		id := pageRegistryIdentity(r.Page, r.Slug)
		if id != "" {
			registeredIDs[strings.ToLower(id)] = true
		}
	}

	wantSlug := registerTargetSlugFromPrompt(prompt)
	candidates := unregisteredPageLiquids(allPaths, registered)
	sort.Strings(candidates)

	var targetPath string
	if wantSlug != "" {
		want := "pages/" + wantSlug + ".liquid"
		if actual, exists := onDisk[want]; exists {
			targetPath = actual
		} else {
			return &ai.Result{
				Summary: fmt.Sprintf(
					"I couldn't register `%s` because `%s` is not on disk. Create the page file first, then ask me to register it.",
					wantSlug, want),
			}, true, nil
		}
		if registered[strings.ToLower(targetPath)] || registeredIDs[wantSlug] {
			return &ai.Result{
				Summary: fmt.Sprintf("`%s` is already registered in `pages.json`. No changes needed.", wantSlug),
			}, true, nil
		}
	} else {
		switch len(candidates) {
		case 0:
			return &ai.Result{
				Summary: "Every on-disk page file is already registered in `pages.json`. Nothing to register.",
			}, true, nil
		case 1:
			targetPath = candidates[0]
		default:
			list := make([]string, 0, len(candidates))
			for _, c := range candidates {
				list = append(list, "`"+c+"`")
			}
			return &ai.Result{
				Summary: "Several unregistered pages were found: " + strings.Join(list, ", ") +
					". Tell me which slug to register (for example: \"register the blog page\").",
				NeedsClarification: true,
			}, true, nil
		}
	}

	slug := pageIDFromLiquidPath(targetPath)
	if slug == "" {
		return nil, false, fmt.Errorf("register existing: invalid page path %q", targetPath)
	}
	if registered[strings.ToLower(targetPath)] || registeredIDs[slug] {
		return &ai.Result{
			Summary: fmt.Sprintf("`%s` is already registered in `pages.json`. No changes needed.", slug),
		}, true, nil
	}

	content, err := store.ReadFile(ctx, auth, targetPath)
	if err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(content) == "" {
		return &ai.Result{
			Summary: fmt.Sprintf("`%s` exists but is empty — refusing to register an empty page file.", targetPath),
		}, true, nil
	}

	entry := normalizeRegistryEntry(&themefs.PageEntry{
		Title:  titleFromSlug(slug),
		Slug:   slug,
		Page:   slug,
		Path:   "/pages",
		Type:   registryTypeForSlug(slug),
		Status: "published",
	})

	merged, added, err := mergePageRegistryEntries(pagesRaw, []*themefs.PageEntry{entry})
	if err != nil {
		return nil, false, err
	}
	if err := validatePagesJSONNonDestructive(pagesRaw, merged, added, true); err != nil {
		return nil, false, err
	}

	result = &ai.Result{
		Summary: fmt.Sprintf(
			"Registered existing page `%s` in `pages.json` (deterministic merge — page content was not regenerated). Review the draft, then Apply.",
			slug),
		Files: []ai.GeneratedFile{
			{Path: targetPath, Action: "update", Content: content},
			{Path: pathPagesJSON, Action: "update", Content: merged},
		},
		PageRegistryEntry: entry,
	}
	if err := validatePageFileRegistryConsistency(pagesRaw, result, []string{slug}); err != nil {
		return nil, false, err
	}
	return result, true, nil
}

func unregisteredPageLiquids(allPaths []string, registered map[string]bool) []string {
	var out []string
	for _, p := range allPaths {
		low := strings.ToLower(p)
		if !strings.HasPrefix(low, "pages/") || !strings.HasSuffix(low, ".liquid") {
			continue
		}
		if strings.HasPrefix(low, "pages/css/") || strings.HasPrefix(low, "pages/auth/") {
			continue
		}
		if registered[low] {
			continue
		}
		out = append(out, p)
	}
	return out
}

func titleFromSlug(slug string) string {
	parts := strings.Split(slug, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

func registryTypeForSlug(slug string) string {
	switch strings.ToLower(slug) {
	case "home":
		return "home"
	case "blog":
		return "blog"
	default:
		return "custom"
	}
}

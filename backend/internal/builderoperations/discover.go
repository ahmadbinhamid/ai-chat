package builderoperations

import (
	"path"
	"regexp"
	"sort"
	"strings"

	"ai-chat/internal/themefs"
)

// MatchesRegisterExistingPrompt reports whether the merchant asked to register
// an existing on-disk page (not create a new one).
func MatchesRegisterExistingPrompt(prompt string) bool {
	p := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
	if p == "" {
		return false
	}
	if looksLikeCreatePage(p) {
		return false
	}
	if looksLikeBulkDelete(p) {
		return false
	}
	return registerExistingRe.MatchString(p)
}

var registerExistingRe = regexp.MustCompile(`(?i)(?:` +
	`\bif\s+(?:the\s+)?\w[\w-]*\s+page\s+is\s+not\s+registered\b` +
	`|` +
	`\bif\s+not\s+register\b` +
	`|` +
	`\b(?:can\s+you\s+)?(?:please\s+)?register\s+(?:it|them|this|the\s+\w[\w-]*\s+page|the\s+page|the\s+blog|blog|page)\b` +
	`|` +
	`\bregister\b[\s\S]{0,40}\b(?:pages?\.json|registry|route)\b` +
	`|` +
	`\b(?:not\s+registered|isn'?t\s+registered|missing\s+(?:from\s+)?(?:pages?\.json|registry))\b[\s\S]{0,48}\bregister\b` +
	`|` +
	`\badd\b[\s\S]{0,24}\b(?:to\s+)?pages?\.json\b` +
	`)`)

var (
	createPageHintRe = regexp.MustCompile(`(?i)\b(?:create|make|build|generate|genrate|add)\b[\s\S]{0,48}\b(?:new\s+)?(?:page|blog|landing|contact|about|faq)\b|\bnew\s+(?:page|landing)`)
	bulkDeleteHintRe = regexp.MustCompile(`(?i)\b(?:delete|remove|drop)\b[\s\S]{0,40}\b(?:pages?|orphan|extra|unused)\b`)
)

func looksLikeCreatePage(p string) bool {
	return createPageHintRe.MatchString(p)
}

func looksLikeBulkDelete(p string) bool {
	return bulkDeleteHintRe.MatchString(p)
}

// namedPagePatterns maps merchant wording → pages/{slug}.liquid slug.
var namedPagePatterns = []struct {
	re   *regexp.Regexp
	slug string
}{
	{regexp.MustCompile(`(?i)\bprivacy\b|\bprivate\s*company\b|\bprivatecompany\b`), "privacy"},
	{regexp.MustCompile(`(?i)\bterms?\b|\bt\s*&\s*c\b|\bconditions?\b`), "terms"},
	{regexp.MustCompile(`(?i)\bcookie\b`), "cookie"},
	{regexp.MustCompile(`(?i)\bpricing\b`), "pricing"},
	{regexp.MustCompile(`(?i)\bservices?\b|\bservice\s*page\b`), "services"},
	{regexp.MustCompile(`(?i)\bshop\s*page\b|\bshop\b|\bproducts?\s*page\b|\bcatalog(?:ue)?\b`), "products"},
	{regexp.MustCompile(`(?i)\babout\s*us\b|\babout\s*page\b|\babout\b`), "about-us"},
	{regexp.MustCompile(`(?i)\bcontact\s*us\b|\bcontact\s*page\b|\bcontact\b`), "contact-us"},
	{regexp.MustCompile(`(?i)\bfaq\b`), "faq"},
	{regexp.MustCompile(`(?i)\breturn\s*-?\s*refund|\brefund\b`), "return-refunds"},
	{regexp.MustCompile(`(?i)\bdelivery\b|\bshipping\b`), "delivery-info"},
	{regexp.MustCompile(`(?i)\bhome\s*page\b|\bhomepage\b`), "home"},
	{regexp.MustCompile(`(?i)\bblog\b`), "blog"},
}

func targetSlugFromPrompt(prompt string) string {
	p := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
	for _, np := range namedPagePatterns {
		if np.re.MatchString(p) {
			return np.slug
		}
	}
	if m := regexp.MustCompile(`(?i)\bpages/([a-z0-9][a-z0-9_-]*)`).FindStringSubmatch(p); len(m) == 2 {
		return m[1]
	}
	return ""
}

func targetSlugFromPlan(planTarget string) string {
	t := strings.TrimSpace(strings.ToLower(planTarget))
	if strings.HasPrefix(t, "pages/") && strings.HasSuffix(t, ".liquid") {
		return strings.TrimSuffix(strings.TrimPrefix(t, "pages/"), ".liquid")
	}
	return ""
}

func flattenThemePaths(tree []themefs.FileTreeEntry) []string {
	var out []string
	var walk func([]themefs.FileTreeEntry)
	walk = func(entries []themefs.FileTreeEntry) {
		for _, e := range entries {
			if e.Type == "directory" || len(e.Children) > 0 {
				walk(e.Children)
				continue
			}
			p := e.Path
			if p == "" {
				p = e.Name
			}
			if p != "" {
				out = append(out, p)
			}
		}
	}
	walk(tree)
	sort.Strings(out)
	return out
}

func pageIDFromLiquidPath(relPath string) string {
	low := strings.ToLower(strings.TrimSpace(relPath))
	if !strings.HasPrefix(low, "pages/") || !strings.HasSuffix(low, ".liquid") || strings.HasPrefix(low, "pages/css/") {
		return ""
	}
	return strings.TrimSpace(strings.TrimSuffix(path.Base(low), ".liquid"))
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

func displayPageLabel(slug string) string {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return "page"
	}
	return slug + " page"
}

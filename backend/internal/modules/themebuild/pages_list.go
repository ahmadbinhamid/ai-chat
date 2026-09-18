package themebuild

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// pagesListRe: merchant asks what pages exist / wants pages.json listed.
// Roman Urdu ("kitny pages", "pages ki list", "list return kro") and English.
// Must route to theme_query + local table — never simple_edit that rewrites
// an unrelated component. Keep narrow: bare "sab pages" is NOT a list
// (that appears in "sab pages delete/remove" too).
//
// Critical: do NOT match "show … page" across arbitrary words — that
// false-positives on "images not show on home page" (a slider fix) and
// dumps pages.json instead of editing the hero. list/show/find/return must
// take pages as their near object, not something shown ON a page.
var pagesListRe = regexp.MustCompile(`(?i)(?:` +
	`\b(?:list|show|find|return)\s+(?:me\s+)?(?:the\s+|all\s+|sb\s+|sab\s+)?pages?\b` +
	`|` +
	`\b(?:list|show|find|return)\b[\s\S]{0,24}\b(?:all\s+)?pages?\s*(?:ki\s*list|list|json|\.json|hn|hain|do|please|kro|karo)?\b` +
	`|` +
	`\b(?:list|show|find|read|return|btao|batao)\b[\s\S]{0,48}\b(?:pages\.json|page\.json)\b` +
	`|` +
	`\b(?:pages\.json|page\.json)\b[\s\S]{0,48}\b(?:list|kitn[yei]|count|how\s+many|read|btao|batao)\b` +
	`|` +
	`\b(?:kitn[yei]|kitne|kitny)\s+pages?\b` +
	`|` +
	`\bpages?\s*ki\s*list\b` +
	`|` +
	`\b(?:sb|sab)\s+pages?\s*ki\s*list\b` +
	`|` +
	`\blist\s+return\b` +
	`|` +
	`\bread\b[\s\S]{0,40}\bpages?\.?json\b` +
	`|` +
	`\bhow\s+many\s+pages\b` +
	`)`)

// pageListExcludeRe: clear theme-edit / broken-UI requests that mention
// "page" but are NOT asking for a registry dump (homepage slider, images
// not loading, etc.).
var pageListExcludeRe = regexp.MustCompile(`(?i)(?:` +
	`\bsliders?\b` +
	`|` +
	`\b(?:hero|carousel|banner)\b` +
	`|` +
	`\b(?:images?|imgs?|photos?)\b[\s\S]{0,48}\b(?:not\s+)?(?:load|show|display|appear|broken|missing|blank)\b` +
	`|` +
	`\b(?:not\s+)?(?:load|show|display|appear)\b[\s\S]{0,48}\b(?:images?|imgs?|photos?|sliders?)\b` +
	`|` +
	`\b(?:fix|change|edit|update|redesign)\b[\s\S]{0,40}\b(?:home\s*page|homepage|slider|hero)\b` +
	`)`)

func isPagesListPrompt(prompt string) bool {
	p := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
	// Any mutate verb wins over list — "sab pages delete" must never list.
	if pageMutateRe.MatchString(p) {
		return false
	}
	// Slider / broken-image / home-page edit complaints are never a registry list.
	if pageListExcludeRe.MatchString(p) {
		return false
	}
	return pagesListRe.MatchString(p)
}

// pageMutateRe: delete/remove — must never take the pages-list fast path.
// Covers English + common Roman Urdu spellings; the model still decides
// WHICH pages — this only prevents a false "here's a table" answer.
var pageMutateRe = regexp.MustCompile(`(?i)(?:` +
	`\b(?:delete|remove|unlink|drop|purge|erase|destroy)\b` +
	`|` +
	`\bdel\b` +
	`|` +
	`\b(?:hata\s*do|hata\s*deo|hatao|mita\s*do|mitao|khatam|nikal\s*do)\b` +
	`|` +
	`\b(?:delete|remove|del|hata|mita|dek)\s*kr(?:o|do|na)?\b` +
	`)`)

// isBulkPageDeletePrompt: merchant wants pages/files/components removed.
// Routes to complex_page with pages.json + delete instructions — the MODEL
// picks which paths (any language).
func isBulkPageDeletePrompt(prompt string) bool {
	p := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
	if !pageMutateRe.MatchString(p) {
		return false
	}
	return strings.Contains(p, "page") || strings.Contains(p, "pages.json") ||
		strings.Contains(p, "page.json") || strings.Contains(p, "blog") ||
		strings.Contains(p, "route") || strings.Contains(p, "slug") ||
		strings.Contains(p, "file") || strings.Contains(p, "fies") ||
		strings.Contains(p, "component") || strings.Contains(p, "folder") ||
		strings.Contains(p, "extra") || strings.Contains(p, "orphan")
}

// namedPagePatterns maps merchant wording → pages/{slug}.liquid slug.
// Longer / more specific patterns first.
var namedPagePatterns = []struct {
	re   *regexp.Regexp
	slug string
}{
	{regexp.MustCompile(`(?i)\bprivacy\b|\bprivate\s*company\b|\bprivatecompany\b`), "privacy"},
	{regexp.MustCompile(`(?i)\bterms?\b|\bt\s*&\s*c\b|\bconditions?\b`), "terms"},
	{regexp.MustCompile(`(?i)\bcookie\b`), "cookie"},
	{regexp.MustCompile(`(?i)\bservices?\b|\bservice\s*page\b`), "services"},
	{regexp.MustCompile(`(?i)\bshop\s*page\b|\bshop\b|\bproducts?\s*page\b|\bcatalog(?:ue)?\b`), "products"},
	{regexp.MustCompile(`(?i)\babout\s*us\b|\babout\s*page\b|\babout\b`), "about-us"},
	{regexp.MustCompile(`(?i)\bcontact\s*us\b|\bcontact\s*page\b|\bcontact\b`), "contact-us"},
	{regexp.MustCompile(`(?i)\bfaq\b`), "faq"},
	{regexp.MustCompile(`(?i)\breturn\s*-?\s*refund|\brefund\b`), "return-refunds"},
	{regexp.MustCompile(`(?i)\bdelivery\b|\bshipping\b`), "delivery-info"},
	{regexp.MustCompile(`(?i)\bhome\s*page\b|\bhomepage\b|\bentire\s+home\b|\bwhole\s+home\b`), "home"},
}

// promptNamedPageSlug returns the single page the merchant is talking about
// (e.g. "privacy", "home"), or "" when the prompt is theme-wide / unclear.
// Used to scope edits to that page only — not every SEO/blog page.
func promptNamedPageSlug(prompt string) string {
	p := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
	for _, np := range namedPagePatterns {
		if np.re.MatchString(p) {
			return np.slug
		}
	}
	// Explicit pages/<slug> path in the prompt.
	if m := regexp.MustCompile(`(?i)\bpages/([a-z0-9][a-z0-9_-]*)`).FindStringSubmatch(p); len(m) == 2 {
		return m[1]
	}
	return ""
}

// promptScopesToNamedPage is true when the merchant named one page to edit
// and did NOT also demand a full theme-wide rewrite of every page.
func promptScopesToNamedPage(prompt string) bool {
	slug := promptNamedPageSlug(prompt)
	if slug == "" {
		return false
	}
	p := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
	// Explicit "all/every pages" without "this page only" → theme-wide.
	if complexRe.MatchString(p) && (strings.Contains(p, "all page") || strings.Contains(p, "every page") ||
		strings.Contains(p, "entire theme") || strings.Contains(p, "whole site")) {
		// Still scope THIS turn to the named page when they also named one —
		// "update privacy … and no jpro on any page" → privacy this turn.
		return true
	}
	return true
}

type pagesJSONRow struct {
	Title  string `json:"title"`
	Slug   string `json:"slug"`
	Type   string `json:"type"`
	Page   string `json:"page"`
	Path   string `json:"path"`
	Status string `json:"status"`
}

// formatPagesRegistryTable builds a merchant-facing markdown table from
// pages.json. Deterministic — no model call.
func formatPagesRegistryTable(pagesJSON string) string {
	rows, err := parsePagesJSONRows(pagesJSON)
	if err != nil || len(rows) == 0 {
		if err != nil {
			return "I couldn't parse `pages.json` (" + err.Error() + "). Try again after the theme finishes syncing."
		}
		return "No pages are registered in `pages.json` yet."
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Status != rows[j].Status {
			// published first
			if rows[i].Status == "published" {
				return true
			}
			if rows[j].Status == "published" {
				return false
			}
		}
		return rows[i].Slug < rows[j].Slug
	})

	var b strings.Builder
	fmt.Fprintf(&b, "Yeh site ke registered pages hain (`pages.json` — **%d** total):\n\n", len(rows))
	b.WriteString("| # | Title | Slug | Type | Status | Template |\n")
	b.WriteString("|---|-------|------|------|--------|----------|\n")
	for i, r := range rows {
		title := dashIfEmpty(r.Title)
		slug := dashIfEmpty(r.Slug)
		typ := dashIfEmpty(r.Type)
		status := dashIfEmpty(r.Status)
		page := dashIfEmpty(r.Page)
		if page == "-" && slug != "-" {
			page = slug
		}
		tmpl := "pages/" + page + ".liquid"
		if r.Path != "" && strings.Contains(r.Path, "auth") {
			tmpl = strings.TrimPrefix(r.Path, "/") + "/" + page + ".liquid"
		}
		fmt.Fprintf(&b, "| %d | %s | `%s` | %s | %s | `%s` |\n", i+1, title, slug, typ, status, tmpl)
	}
	b.WriteString("\nBatao kis page pe kaam karna hai — sirf usi page ki files edit hongi.")
	return b.String()
}

func dashIfEmpty(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "-"
	}
	return s
}

func parsePagesJSONRows(raw string) ([]pagesJSONRow, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var arr []pagesJSONRow
	if err := json.Unmarshal([]byte(raw), &arr); err == nil {
		return arr, nil
	}
	// Some themes wrap as {"pages":[...]} or {"data":[...]}.
	var wrap map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &wrap); err != nil {
		return nil, err
	}
	for _, key := range []string{"pages", "data", "items"} {
		if v, ok := wrap[key]; ok {
			if err := json.Unmarshal(v, &arr); err == nil {
				return arr, nil
			}
		}
	}
	return nil, fmt.Errorf("unexpected pages.json shape")
}

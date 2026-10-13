// Package prefetch picks the theme files a turn will almost certainly read, so they can be sent with the prompt
// instead of costing the model exploration round trips. Pure: every read goes through the caller's ReadFunc.
package prefetch

import (
	"context"
	"encoding/json"
	"path"
	"regexp"
	"strings"

	"ai-chat/internal/pageintent"
	"ai-chat/internal/themefs"
)

const (
	MaxFiles = 8
	MaxBytes = 40_000
)

// Reason says why a file was picked, in priority order.
type Reason string

const (
	ReasonRoutePage Reason = "route_page"
	ReasonFocusFile Reason = "focus_file"
	ReasonRendered  Reason = "rendered"
	ReasonNamedPage Reason = "named_page"
)

type Input struct {
	// Route is the storefront route in the preview as the dashboard shows it ("" is home); ignored unless HasRoute.
	Route     string
	HasRoute  bool
	FocusFile string
	PagesJSON string
	Prompt    string
	// Skip, when set, excludes paths the caller knows must not be sent (e.g. draft-only staged images).
	Skip func(path string) bool
}

// ReadFunc returns a file's content, or "" when it doesn't exist.
type ReadFunc func(ctx context.Context, path string) (string, error)

type File struct {
	Path    string
	Content string
	Reason  Reason
}

type Result struct {
	Files []File
	Bytes int
}

var textExt = map[string]bool{".liquid": true, ".css": true, ".js": true, ".json": true}

// Already in the model's context every turn, so preloading them would only duplicate it.
var alwaysInContext = map[string]bool{"pages.json": true, "defaults.json": true}

// Select reads candidates in priority order (route page, focus file, what those render, pages the prompt names) and
// keeps each one that fits within MaxFiles and MaxBytes.
func Select(ctx context.Context, in Input, read ReadFunc) Result {
	s := selector{in: in, read: read, seen: map[string]bool{}}
	entries := parsePages(in.PagesJSON)

	var rendered []string
	if in.HasRoute {
		if content, ok := s.add(ctx, entryPathForRoute(in.Route, entries), ReasonRoutePage); ok {
			rendered = append(rendered, RenderedPaths(content)...)
		}
	}
	if in.FocusFile != "" {
		if content, ok := s.add(ctx, in.FocusFile, ReasonFocusFile); ok {
			rendered = append(rendered, RenderedPaths(content)...)
		}
	}
	for _, p := range rendered {
		s.add(ctx, p, ReasonRendered)
	}
	for _, p := range namedPagePaths(in.Prompt, entries) {
		s.add(ctx, p, ReasonNamedPage)
	}
	return s.result
}

type selector struct {
	in     Input
	read   ReadFunc
	seen   map[string]bool
	result Result
}

// add reads p and keeps it if it fits; ok reports a readable file even when the caps left it out, so the files it
// renders are still considered.
func (s *selector) add(ctx context.Context, p string, reason Reason) (content string, ok bool) {
	if !s.eligible(p) {
		return "", false
	}
	s.seen[p] = true
	content, err := s.read(ctx, p)
	if err != nil || content == "" {
		return "", false
	}
	if len(s.result.Files) < MaxFiles && s.result.Bytes+len(content) <= MaxBytes {
		s.result.Files = append(s.result.Files, File{Path: p, Content: content, Reason: reason})
		s.result.Bytes += len(content)
	}
	return content, true
}

func (s *selector) eligible(p string) bool {
	if p == "" || s.seen[p] || alwaysInContext[p] || !textExt[path.Ext(p)] {
		return false
	}
	if themefs.ValidatePathSafety(p) != nil {
		return false
	}
	return s.in.Skip == nil || !s.in.Skip(p)
}

type pageEntry struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
	Path  string `json:"path"`
	Page  string `json:"page"`
}

// parsePages reads pages.json; a malformed registry is treated as empty, since this is only an optimisation.
func parsePages(pagesJSON string) []pageEntry {
	var entries []pageEntry
	if json.Unmarshal([]byte(pagesJSON), &entries) != nil {
		return nil
	}
	return entries
}

func (e pageEntry) filePath() string {
	dir := strings.Trim(e.Path, "/")
	if dir == "" || e.Page == "" {
		return ""
	}
	return dir + "/" + e.Page + ".liquid"
}

// Mirrors flowpos-backend's PageResolver::RESOURCE_ROUTES, as the dashboard preview's entryPathForRoute does.
var resourceRouteRe = regexp.MustCompile(`^(product|category)/[^/]+$`)

// entryPathForRoute maps a preview route to the page file it renders; it must agree with the dashboard's
// entryPathForRoute (usePreviewDoc.ts) so the AI is shown the page the merchant is looking at.
func entryPathForRoute(route string, pagesJSON []pageEntry) string {
	trimmed := strings.TrimLeft(strings.SplitN(strings.SplitN(strings.TrimSpace(route), "#", 2)[0], "?", 2)[0], "/")
	isAuth := strings.HasPrefix(trimmed, "auth/")
	resource := resourceRouteRe.FindStringSubmatch(trimmed)
	slug := trimmed
	switch {
	case trimmed == "":
		slug = "home"
	case isAuth:
		slug = strings.TrimPrefix(trimmed, "auth/")
	case resource != nil:
		slug = resource[1]
	}
	for _, e := range pagesJSON {
		if e.Slug == slug && e.Page != "" {
			if p := e.filePath(); p != "" {
				return p
			}
		}
	}
	switch {
	case trimmed == "":
		return "pages/home.liquid"
	case isAuth:
		return "pages/auth/" + slug + ".liquid"
	default:
		return "pages/" + slug + ".liquid"
	}
}

var renderTagRe = regexp.MustCompile(`\{%-?\s*(?:render|include)\s+['"]([^'"]+)['"]`)

// RenderedPaths lists the files content renders or includes (one level), in order, without duplicates.
func RenderedPaths(content string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range renderTagRe.FindAllStringSubmatch(content, -1) {
		p := m[1]
		if !strings.HasSuffix(p, ".liquid") {
			p += ".liquid"
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// namedPagePaths returns the page files of registered pages the prompt names explicitly: "<name> page",
// "page <name>" or "/<slug>". A bare word like "products" is not a page reference.
func namedPagePaths(prompt string, pagesJSON []pageEntry) []string {
	words := "-" + pageintent.Slugify(prompt) + "-"
	lower := strings.ToLower(prompt)
	var out []string
	for _, e := range pagesJSON {
		p := e.filePath()
		if p == "" {
			continue
		}
		if namedIn(words, lower, e) {
			out = append(out, p)
		}
	}
	return out
}

func namedIn(words, lower string, e pageEntry) bool {
	for _, name := range []string{pageintent.Slugify(e.Slug), pageintent.Slugify(e.Title)} {
		if name == "" {
			continue
		}
		if strings.Contains(words, "-"+name+"-page-") || strings.Contains(words, "-page-"+name+"-") {
			return true
		}
	}
	return e.Slug != "" && slashRouteRe(e.Slug).MatchString(lower)
}

func slashRouteRe(slug string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[\s("'])/` + regexp.QuoteMeta(strings.ToLower(slug)) + `(?:$|[\s)"'.,!?/])`)
}

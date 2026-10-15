// Package liquiddeps finds the theme files one Liquid template pulls in, so a preview can load a page's files
// instead of the whole theme. It mirrors how the dashboard's LiquidJS engine resolves them.
package liquiddeps

import (
	"regexp"
	"sort"
	"strings"
)

var (
	tagRe          = regexp.MustCompile(`(?s)\{%-?\s*([a-zA-Z_]+)(.*?)-?%\}`)
	literalArgRe   = regexp.MustCompile(`^\s*(?:'([^']*)'|"([^"]*)")`)
	assetLiteralRe = regexp.MustCompile(`(?:'([^'{}]*)'|"([^"{}]*)")\s*\|\s*asset_url\b`)
	assetFilterRe  = regexp.MustCompile(`\|\s*asset_url\b`)
	assetPathRe    = regexp.MustCompile(`/theme-assets/([^"'\s?#{}<>]+)`)
)

// References returns the theme-relative paths content renders or links: partials as "<name>.liquid" (LiquidJS's
// in-memory fs always appends the extension) and .css/.js assets. dynamic is true when a reference can't be resolved
// statically; the caller must then load the whole theme, since anything could be pulled in.
func References(content string) (refs []string, dynamic bool) {
	seen := map[string]bool{}
	add := func(p string) {
		if p != "" {
			seen[p] = true
		}
	}

	for _, m := range tagRe.FindAllStringSubmatch(content, -1) {
		switch tag, args := m[1], m[2]; tag {
		case "render", "include":
			name, ok := literalArg(args)
			if !ok || !staticName(name) {
				dynamic = true
				continue
			}
			add(name + ".liquid")
		case "layout":
			name, ok := literalArg(args)
			if !ok {
				if strings.TrimSpace(args) != "none" {
					dynamic = true
				}
				continue
			}
			if !staticName(name) {
				dynamic = true
				continue
			}
			add(name + ".liquid")
		case "liquid":
			// Its body holds tags without delimiters, which this scanner doesn't parse.
			dynamic = true
		}
	}

	literals := assetLiteralRe.FindAllStringSubmatch(content, -1)
	if len(literals) != len(assetFilterRe.FindAllStringIndex(content, -1)) {
		dynamic = true
	}
	for _, m := range literals {
		add(scriptOrStyle(m[1] + m[2]))
	}
	for _, m := range assetPathRe.FindAllStringSubmatch(content, -1) {
		add(scriptOrStyle(m[1]))
	}

	refs = make([]string, 0, len(seen))
	for p := range seen {
		refs = append(refs, p)
	}
	sort.Strings(refs)
	return refs, dynamic
}

func literalArg(args string) (string, bool) {
	m := literalArgRe.FindStringSubmatch(args)
	if m == nil {
		return "", false
	}
	return m[1] + m[2], true
}

// staticName rejects names LiquidJS would resolve relative to the including file, or that hold Liquid themselves.
func staticName(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "{{") || strings.Contains(name, "\\") {
		return false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "." || seg == ".." || seg == "" {
			return false
		}
	}
	return true
}

// scriptOrStyle keeps only .css/.js paths, the asset kinds the preview inlines from the draft.
func scriptOrStyle(p string) string {
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if strings.HasSuffix(p, ".css") || strings.HasSuffix(p, ".js") {
		return p
	}
	return ""
}

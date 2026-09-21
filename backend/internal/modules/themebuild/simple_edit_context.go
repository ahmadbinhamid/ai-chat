package themebuild

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"ai-chat/internal/ai"
	"ai-chat/internal/themefs"
)

// SimpleEditContext is the CPU-local package prepared before DeepSeek for
// IntentSimpleEdit — discovery happens here, not in the model tool loop.
type SimpleEditContext struct {
	Targets    []string
	Paths      []string
	Package    string // injected into the user prompt
	Sufficient bool   // true → propose-only tools; false → allow one read
}

const (
	maxSimpleEditPaths      = 2
	maxSimpleEditPkgRunes   = 12_000
	maxSimpleEditModelCalls = 2
	simpleEditExcerptHead   = 40
	simpleEditExcerptTail   = 30
	simpleEditMaxLines      = 90
	simpleEditFocusWindow   = 50 // lines around a keyword hit
)

// BuildSimpleEditContext picks likely theme files for a simple edit prompt
// and builds focused excerpts from store (local workspace / overlay).
func BuildSimpleEditContext(ctx context.Context, store themefs.ThemeStore, auth themefs.RequestAuth, prompt string) (SimpleEditContext, error) {
	out := SimpleEditContext{Targets: detectSimpleEditTargets(prompt)}
	tree, err := store.ListFiles(ctx, auth)
	if err != nil {
		return out, err
	}
	paths := flattenThemePaths(tree)
	ranked := rankPathsForTargets(paths, out.Targets, prompt)
	if len(ranked) == 0 {
		out.Package = simpleEditFallbackPackage(prompt, paths)
		out.Sufficient = false
		return out, nil
	}
	if len(ranked) > maxSimpleEditPaths {
		ranked = ranked[:maxSimpleEditPaths]
	}
	out.Paths = ranked

	var b strings.Builder
	b.WriteString("## Pre-selected local edit context\n")
	b.WriteString("You are modifying an existing theme. Relevant files have already been selected locally.\n")
	b.WriteString("Do not search for files. Do not explain your reasoning. Do not regenerate whole files.\n")
	b.WriteString("Return only a minimal propose_changes changeset (prefer action \"edit\" with old_string/new_string).\n\n")
	b.WriteString("Merchant request: ")
	b.WriteString(strings.TrimSpace(prompt))
	b.WriteString("\n\n")

	for _, p := range ranked {
		content, rerr := store.ReadFile(ctx, auth, p)
		if rerr != nil {
			fmt.Fprintf(&b, "### %s\nERROR: %v\n\n", p, rerr)
			continue
		}
		excerpt := excerptForSimpleEdit(p, content, prompt)
		fmt.Fprintf(&b, "### %s\n%s\n", p, excerpt)
		if len([]rune(b.String())) > maxSimpleEditPkgRunes {
			b.WriteString("\n(additional candidate files omitted to keep context bounded)\n")
			break
		}
	}
	out.Package = b.String()
	// Enough when we have at least one non-error body and a named target.
	out.Sufficient = len(out.Paths) > 0 && len(out.Targets) > 0
	return out, nil
}

func detectSimpleEditTargets(prompt string) []string {
	p := strings.ToLower(prompt)
	var targets []string
	add := func(t string) {
		for _, x := range targets {
			if x == t {
				return
			}
		}
		targets = append(targets, t)
	}
	checks := []struct {
		keys   []string
		target string
	}{
		{[]string{"header", "hdr", "navbar", "nav bar", "top bar"}, "header"},
		{[]string{"footer"}, "footer"},
		{[]string{"hero", "banner"}, "hero"},
		{[]string{"slider", "carousel"}, "slider"},
		{[]string{"product card", "product-card", "cards"}, "product_card"},
		{[]string{"homepage", "home page", "landing"}, "homepage"},
		{[]string{"button", "btn", "cta"}, "button"},
		{[]string{"logo"}, "logo"},
		{[]string{"cart"}, "cart"},
		{[]string{"menu"}, "menu"},
		{[]string{"sidebar"}, "sidebar"},
		{[]string{"section"}, "section"},
	}
	for _, c := range checks {
		for _, k := range c.keys {
			if strings.Contains(p, k) {
				add(c.target)
				break
			}
		}
	}
	return targets
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
			if themeworkspaceAIText(p) {
				out = append(out, p)
			}
		}
	}
	walk(tree)
	sort.Strings(out)
	return out
}

func themeworkspaceAIText(rel string) bool {
	ext := strings.ToLower(path.Ext(rel))
	switch ext {
	case ".liquid", ".css", ".scss", ".sass", ".js", ".ts", ".json", ".svg":
		return true
	default:
		base := path.Base(rel)
		return base == "defaults.json" || base == "pages.json"
	}
}

func rankPathsForTargets(paths, targets []string, prompt string) []string {
	type scored struct {
		path  string
		score int
	}
	var ranked []scored
	p := strings.ToLower(prompt)
	for _, fp := range paths {
		low := strings.ToLower(fp)
		base := strings.ToLower(path.Base(fp))
		score := 0
		for _, t := range targets {
			switch t {
			case "header":
				if strings.Contains(low, "header") || strings.Contains(low, "hdr") || strings.Contains(low, "nav") {
					score += 100
				}
			case "footer":
				if strings.Contains(low, "footer") {
					score += 100
				}
			case "hero":
				if strings.Contains(low, "hero") || strings.Contains(low, "banner") {
					score += 100
				}
			case "slider":
				if strings.Contains(low, "slider") || strings.Contains(low, "carousel") {
					score += 100
				}
				if strings.Contains(low, "hero") || strings.Contains(low, "banner") {
					score += 40
				}
			case "product_card":
				if strings.Contains(low, "product") && (strings.Contains(low, "card") || strings.Contains(low, "item") || strings.Contains(low, "grid")) {
					score += 100
				}
				if strings.Contains(low, "product-card") || strings.Contains(low, "product_card") {
					score += 40
				}
			case "homepage":
				if strings.Contains(low, "pages/home") || base == "home.liquid" || strings.Contains(low, "index") {
					score += 90
				}
			case "button":
				if strings.Contains(low, "button") || strings.Contains(low, "btn") || strings.Contains(low, "cta") {
					score += 80
				}
				// Buttons often live in header — soft boost. Prefer header.css
				// when the merchant names both header + button (color edits).
				if strings.Contains(low, "header") {
					score += 30
					if strings.HasSuffix(low, ".css") {
						score += 40
					}
				}
			case "logo", "cart", "menu", "sidebar", "section":
				if strings.Contains(low, t) {
					score += 80
				}
			}
		}
		if strings.HasSuffix(low, ".css") || strings.HasSuffix(low, ".scss") {
			for _, t := range targets {
				if t != "" && strings.Contains(low, strings.ReplaceAll(t, "_", "-")) {
					score += 25
				}
			}
		}
		// Prefer Liquid structure over CSS for the primary edit surface.
		if strings.HasSuffix(low, ".liquid") {
			score += 35
		}
		// Exact basename hits (header.liquid) beat header-menu.css noise.
		for _, t := range targets {
			tDash := strings.ReplaceAll(t, "_", "-")
			if base == tDash+".liquid" || base == t+".liquid" || strings.HasPrefix(base, tDash+".") {
				score += 50
			}
		}
		if strings.Contains(p, "color") || strings.Contains(p, "background") || strings.Contains(p, "dark") {
			if strings.HasSuffix(low, ".css") || strings.HasSuffix(low, ".scss") || strings.Contains(low, "defaults.json") {
				score += 20
			}
		}
		if score > 0 {
			ranked = append(ranked, scored{fp, score})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].path < ranked[j].path
	})
	out := make([]string, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, r.path)
	}
	return out
}

func simpleEditFallbackPackage(prompt string, paths []string) string {
	var candidates []string
	for _, p := range paths {
		low := strings.ToLower(p)
		if strings.Contains(low, "header") || strings.Contains(low, "home") ||
			strings.Contains(low, "hero") || strings.Contains(low, "slider") ||
			strings.Contains(low, "carousel") || strings.Contains(low, "product") {
			candidates = append(candidates, p)
		}
		if len(candidates) >= 12 {
			break
		}
	}
	var b strings.Builder
	b.WriteString("## Pre-selected local edit context\n")
	b.WriteString("Exact file content was not pre-loaded. Candidate paths from the local tree:\n")
	for _, c := range candidates {
		fmt.Fprintf(&b, "- %s\n", c)
	}
	b.WriteString("\nMerchant request: ")
	b.WriteString(strings.TrimSpace(prompt))
	b.WriteString("\nCall propose_changes if you can act from defaults.json + these paths; otherwise one read_theme_file is allowed.\n")
	return b.String()
}

func simpleEditOneShotPrompt(userPrompt string, sec SimpleEditContext) string {
	var b strings.Builder
	if sec.Package != "" {
		b.WriteString(sec.Package)
		b.WriteString("\n")
	} else {
		b.WriteString(userPrompt)
		b.WriteString("\n\n")
	}
	b.WriteString("[SIMPLE_EDIT] Constraints:\n")
	b.WriteString("- Call propose_changes once with a minimal changeset.\n")
	b.WriteString("- Prefer action \"edit\" (old_string/new_string). Never re-emit an entire unchanged file as \"update\".\n")
	b.WriteString("- Touch at most 2 files. Keep patches small (CSS color/spacing/class tweaks preferred).\n")
	b.WriteString("- summary: 1 short merchant sentence only. No tutorials, no reasoning, no design essays.\n")
	b.WriteString("- Do not call exploration tools. Files above are authoritative.\n")
	b.WriteString("- needs_clarification only if you truly cannot act safely.\n")
	return b.String()
}

func filterFileTreeToPaths(tree []themefs.FileTreeEntry, keep []string) []themefs.FileTreeEntry {
	if len(keep) == 0 {
		return tree
	}
	want := map[string]bool{}
	for _, p := range keep {
		want[p] = true
	}
	var out []themefs.FileTreeEntry
	var walk func([]themefs.FileTreeEntry)
	walk = func(entries []themefs.FileTreeEntry) {
		for _, e := range entries {
			if len(e.Children) > 0 {
				walk(e.Children)
				continue
			}
			p := e.Path
			if p == "" {
				p = e.Name
			}
			if want[p] {
				out = append(out, themefs.FileTreeEntry{Name: e.Name, Path: p, Type: "file"})
			}
		}
	}
	walk(tree)
	if len(out) == 0 {
		return tree
	}
	return out
}

// excerptForSimpleEdit picks a focused window (keyword-aware when possible)
// with explicit line ranges — never the full 1398-line file.
func excerptForSimpleEdit(pathName, body, prompt string) string {
	lines := strings.Split(body, "\n")
	n := len(lines)
	if n > 0 && lines[n-1] == "" {
		n--
		lines = lines[:n]
	}
	if n == 0 {
		return "(empty file)\n"
	}
	if n <= simpleEditMaxLines {
		var b strings.Builder
		fmt.Fprintf(&b, "(lines 1–%d of %d)\n", n, n)
		b.WriteString(strings.Join(lines, "\n"))
		b.WriteByte('\n')
		return b.String()
	}

	// Keyword window from the merchant prompt (color/background/header/…).
	if start, end, ok := focusWindow(lines, prompt); ok {
		var b strings.Builder
		fmt.Fprintf(&b, "(lines %d–%d of %d — focused section)\n", start+1, end, n)
		b.WriteString(strings.Join(lines[start:end], "\n"))
		b.WriteByte('\n')
		return b.String()
	}

	ex := ai.ExcerptThemeBody(pathName, body)
	exLines := strings.Split(ex, "\n")
	en := len(exLines)
	if en > 0 && exLines[en-1] == "" {
		en--
		exLines = exLines[:en]
	}
	if en <= simpleEditMaxLines {
		var b strings.Builder
		fmt.Fprintf(&b, "(structural excerpt of %d-line file)\n", n)
		b.WriteString(strings.Join(exLines, "\n"))
		b.WriteByte('\n')
		return b.String()
	}
	head, tail := simpleEditExcerptHead, simpleEditExcerptTail
	var b strings.Builder
	fmt.Fprintf(&b, "(lines 1–%d and %d–%d of %d)\n", head, n-tail+1, n, n)
	b.WriteString(strings.Join(lines[:head], "\n"))
	b.WriteString("\n\n…\n\n")
	b.WriteString(strings.Join(lines[n-tail:], "\n"))
	b.WriteByte('\n')
	return b.String()
}

func focusWindow(lines []string, prompt string) (start, end int, ok bool) {
	keys := focusKeywords(prompt)
	if len(keys) == 0 {
		return 0, 0, false
	}
	best := -1
	for i, line := range lines {
		low := strings.ToLower(line)
		for _, k := range keys {
			if strings.Contains(low, k) {
				best = i
				break
			}
		}
		if best >= 0 {
			break
		}
	}
	if best < 0 {
		return 0, 0, false
	}
	half := simpleEditFocusWindow / 2
	start = best - half
	if start < 0 {
		start = 0
	}
	end = start + simpleEditFocusWindow
	if end > len(lines) {
		end = len(lines)
		start = end - simpleEditFocusWindow
		if start < 0 {
			start = 0
		}
	}
	return start, end, true
}

func focusKeywords(prompt string) []string {
	p := strings.ToLower(prompt)
	var keys []string
	add := func(k string) {
		for _, x := range keys {
			if x == k {
				return
			}
		}
		keys = append(keys, k)
	}
	for _, k := range []string{
		"background", "color", "colour", "button", "btn", "nav", "menu",
		"header", "hero", "banner", "slider", "carousel", "autoplay",
		"card", "hover", "padding", "margin",
	} {
		if strings.Contains(p, k) {
			add(k)
		}
	}
	return keys
}

func truncateForSimpleEditPrompt(s string, maxRunes int) string {
	r := []rune(strings.TrimSpace(s))
	if maxRunes <= 0 || len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "\n…(truncated for simple-edit)"
}

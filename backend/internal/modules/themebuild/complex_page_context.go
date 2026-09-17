package themebuild

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"ai-chat/internal/themefs"
)

// ComplexPageContext is CPU-local context for IntentComplexPage (new page /
// page+menu / homepage redesign). Discovery happens here so DeepSeek does
// not thrash on list/grep/read before proposing.
type ComplexPageContext struct {
	Paths      []string
	Package    string
	Sufficient bool // true when ranked paths cover the request class
}

const (
	maxComplexPagePaths      = 5
	maxComplexPagePkgRunes   = 18_000
	maxComplexPageModelCalls = 4 // prepared path should propose quickly
	maxComplexExploration    = 1 // at most one narrow read before force-propose
	maxComplexExploreStreak  = 1 // one explore-only turn, then force propose
	complexPageExcerptLines  = 80
	// complexPageRecentTurns is how many prior chat turns to replay when
	// PageCreatePrepared — skip Summarize API cost when local theme context
	// already carries the structural state.
	complexPageRecentTurns = 4
)

type pathScore struct {
	path  string
	score int
}

// BuildComplexPageContext selects a compact, intent-aware file package —
// homepage redesign prefers home/hero/slider; page+menu prefers pages + nav;
// never a full theme dump.
func BuildComplexPageContext(ctx context.Context, store themefs.ThemeStore, auth themefs.RequestAuth, prompt string) (ComplexPageContext, error) {
	out := ComplexPageContext{}
	tree, err := store.ListFiles(ctx, auth)
	if err != nil {
		return out, err
	}
	paths := flattenThemePaths(tree)
	ranked := rankPathsForPageCreate(paths, prompt)
	if len(ranked) > maxComplexPagePaths {
		ranked = ranked[:maxComplexPagePaths]
	}
	out.Paths = ranked
	homeRedesign := isHomePageRedesignPrompt(prompt)
	out.Sufficient = complexPageContextSufficient(ranked, homeRedesign)

	var b strings.Builder
	if homeRedesign {
		b.WriteString("## Pre-selected local homepage-redesign context\n")
		b.WriteString("Redesign the homepage (slider/hero/layout as requested).\n")
		b.WriteString("Relevant homepage files were selected locally — call propose_changes promptly.\n")
		b.WriteString("Do not re-list or grep the theme. Only change files required for the homepage redesign.\n")
		b.WriteString("Emit a compact changeset: only changed files, no full-file dumps of unchanged assets, one short summary.\n")
		b.WriteString("Typical changeset: pages/home.liquid (+ matching CSS/JS) and any hero/slider partials.\n\n")
	} else {
		b.WriteString("## Pre-selected local page-create context\n")
		b.WriteString("Create or register a new page and wire navigation if requested.\n")
		b.WriteString("Relevant theme conventions were selected locally — call propose_changes promptly.\n")
		b.WriteString("Do not dump unrelated files. Only change files that are required.\n")
		b.WriteString("Emit a compact changeset: only changed files, one short summary.\n")
		b.WriteString("Typical changeset: new page liquid + pages.json entry + menu/nav/header link.\n\n")
	}
	b.WriteString("Merchant request: ")
	b.WriteString(strings.TrimSpace(prompt))
	b.WriteString("\n\n")

	for _, p := range ranked {
		content, rerr := store.ReadFile(ctx, auth, p)
		if rerr != nil {
			fmt.Fprintf(&b, "### %s\nERROR: %v\n\n", p, rerr)
			continue
		}
		excerpt := truncateLines(content, complexPageExcerptLines)
		fmt.Fprintf(&b, "### %s\n%s\n\n", p, excerpt)
		if len([]rune(b.String())) > maxComplexPagePkgRunes {
			b.WriteString("(additional files omitted to keep context bounded)\n")
			break
		}
	}
	if len(ranked) == 0 {
		b.WriteString("(no ranked files — one targeted read_theme_file if needed, then propose_changes)\n")
	}
	out.Package = b.String()
	return out, nil
}

func complexPagePreparedPrompt(userPrompt string, cpc ComplexPageContext) string {
	if strings.TrimSpace(cpc.Package) == "" {
		return userPrompt
	}
	return cpc.Package + "\n---\n" + strings.TrimSpace(userPrompt)
}

func isHomePageRedesignPrompt(prompt string) bool {
	return pageRedesignRe.MatchString(prompt)
}

func promptWantsHeaderOrNav(prompt string) bool {
	p := strings.ToLower(prompt)
	if strings.Contains(p, "header") || strings.Contains(p, "nav") {
		return true
	}
	return menuNavRe.MatchString(p)
}

func complexPageContextSufficient(paths []string, homeRedesign bool) bool {
	if len(paths) == 0 {
		return false
	}
	if homeRedesign {
		for _, p := range paths {
			low := strings.ToLower(p)
			if strings.Contains(low, "pages/home") && strings.HasSuffix(low, ".liquid") {
				return true
			}
			if strings.Contains(low, "hero") || strings.Contains(low, "slider") || strings.Contains(low, "carousel") {
				return true
			}
			if strings.Contains(low, "home") && (strings.HasSuffix(low, ".css") || strings.HasSuffix(low, ".js") || strings.HasSuffix(low, ".liquid")) {
				return true
			}
		}
		return false
	}
	for _, p := range paths {
		base := strings.ToLower(path.Base(p))
		low := strings.ToLower(p)
		if base == "pages.json" {
			return true
		}
		if strings.Contains(low, "pages/") && strings.HasSuffix(low, ".liquid") {
			return true
		}
	}
	return false
}

func rankPathsForPageCreate(paths []string, prompt string) []string {
	p := strings.ToLower(prompt)
	homeRedesign := isHomePageRedesignPrompt(prompt)
	wantsNav := promptWantsHeaderOrNav(prompt)
	var ranked []pathScore
	seen := map[string]bool{}
	for _, fp := range paths {
		low := strings.ToLower(fp)
		base := strings.ToLower(path.Base(fp))
		score := 0
		switch {
		case base == "pages.json":
			score = 200
		case strings.Contains(low, "pages/") && strings.HasSuffix(low, ".liquid"):
			if strings.Contains(low, "home") || strings.Contains(low, "about") || strings.Contains(low, "contact") {
				score = 120
			} else {
				score = 90
			}
		case strings.Contains(low, "header") || strings.Contains(low, "nav") || strings.Contains(low, "menu"):
			score = 150
		case strings.Contains(low, "layout") && strings.HasSuffix(low, ".liquid"):
			score = 70
		case base == "defaults.json":
			score = 40
		}
		if homeRedesign {
			switch {
			case strings.Contains(low, "pages/home") && strings.HasSuffix(low, ".liquid"):
				score = 260
			case strings.Contains(low, "home") && (strings.HasSuffix(low, ".css") || strings.HasSuffix(low, ".js")):
				score = 220
			case strings.Contains(low, "hero") || strings.Contains(low, "slider") || strings.Contains(low, "carousel"):
				score = 210
			case base == "pages.json":
				score = 160
			case strings.Contains(low, "layout") && strings.HasSuffix(low, ".liquid"):
				score = 50
			case strings.Contains(low, "header") || strings.Contains(low, "nav") || strings.Contains(low, "menu"):
				if wantsNav {
					score = 90
				} else {
					score = 0
				}
			}
		} else if !wantsNav {
			// Page create without an explicit menu/nav ask: keep one nav file
			// useful for wiring, but do not flood the package with headers.
			if strings.Contains(low, "header") || strings.Contains(low, "nav") || strings.Contains(low, "menu") {
				if score > 110 {
					score = 110
				}
			}
		}
		if strings.Contains(p, "contact") && strings.Contains(low, "contact") {
			score += 30
		}
		if strings.Contains(p, "faq") && strings.Contains(low, "faq") {
			score += 30
		}
		if strings.Contains(p, "about") && strings.Contains(low, "about") {
			score += 30
		}
		if score > 0 && !seen[fp] {
			seen[fp] = true
			ranked = append(ranked, pathScore{fp, score})
		}
	}

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].path < ranked[j].path
		}
		return ranked[i].score > ranked[j].score
	})

	var out []string
	pageSample := 0
	headerSample := 0
	for _, r := range ranked {
		low := strings.ToLower(r.path)
		if strings.Contains(low, "pages/") && strings.HasSuffix(low, ".liquid") {
			if pageSample >= 1 {
				continue
			}
			pageSample++
		}
		if homeRedesign && (strings.Contains(low, "header") || strings.Contains(low, "nav") || strings.Contains(low, "menu")) {
			if headerSample >= 1 {
				continue
			}
			headerSample++
		}
		out = append(out, r.path)
		if len(out) >= maxComplexPagePaths {
			break
		}
	}
	return out
}

func truncateLines(content string, maxLines int) string {
	if maxLines <= 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= maxLines {
		return content
	}
	return strings.Join(lines[:maxLines], "\n") + "\n…(truncated)"
}

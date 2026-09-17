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
// page+menu). Discovery happens here so DeepSeek does not thrash on
// list/grep/read before proposing.
type ComplexPageContext struct {
	Paths   []string
	Package string
}

const (
	maxComplexPagePaths      = 5
	maxComplexPagePkgRunes   = 18_000
	maxComplexPageModelCalls = 8 // bounded tool-loop for page create
	maxComplexExploration    = 6 // exploration tool calls before force-propose
	maxComplexExploreStreak  = 3 // exploration-only iterations before force-propose
	complexPageExcerptLines  = 80
)

type pathScore struct {
	path  string
	score int
}

// BuildComplexPageContext selects pages.json, a representative page, and
// menu/nav/header files — compact, not a full theme dump.
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

	var b strings.Builder
	b.WriteString("## Pre-selected local page-create context\n")
	b.WriteString("Create or register a new page and wire navigation if requested.\n")
	b.WriteString("Relevant theme conventions were selected locally — prefer propose_changes soon.\n")
	b.WriteString("Do not dump unrelated files. Only change files that are required.\n")
	b.WriteString("Typical changeset: new page liquid + pages.json entry + menu/nav/header link.\n\n")
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
		b.WriteString("(no ranked files — use list/read tools sparingly, then propose_changes)\n")
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

func rankPathsForPageCreate(paths []string, prompt string) []string {
	p := strings.ToLower(prompt)
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
	for _, r := range ranked {
		low := strings.ToLower(r.path)
		if strings.Contains(low, "pages/") && strings.HasSuffix(low, ".liquid") {
			if pageSample >= 1 {
				continue
			}
			pageSample++
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

package ai

import (
	"fmt"
	"strings"
)

// Interactive read_theme_file results are capped before they re-enter the
// model messages array. Measured header.liquid (~1398 lines) added ~15.5k
// effective tokens to the next DeepSeek call; full 40KB bodies are too large
// for interactive edit turns. The on-disk/FlowPOS read is unchanged — only
// the model-facing string is excerpted (UI line counts still use the raw
// tool output via summarizeToolResult before this runs).
const (
	maxInteractiveReadLines = 220
	readExcerptHeadLines    = 90
	readExcerptTailLines    = 60
	maxOutlineLines         = 40
)

// excerptReadThemeFileResult walks execReadThemeFile's "### path\nbody\n\n"
// sections and replaces oversized bodies with a line-aware excerpt: structural
// outline + head + tail + an explicit omission marker so the model can request
// a narrower follow-up read.
func excerptReadThemeFileResult(output string) string {
	if output == "" || strings.Count(output, "\n") <= maxInteractiveReadLines {
		return output
	}
	sections := splitReadThemeSections(output)
	if len(sections) == 0 {
		return excerptSingleBody("", output)
	}
	var b strings.Builder
	for _, sec := range sections {
		body := excerptSingleBody(sec.path, sec.body)
		if sec.path == "" {
			b.WriteString(body)
			if !strings.HasSuffix(body, "\n") {
				b.WriteByte('\n')
			}
			continue
		}
		fmt.Fprintf(&b, "### %s\n%s", sec.path, body)
		if !strings.HasSuffix(body, "\n") {
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return b.String()
}

type readSection struct {
	path string
	body string
}

func splitReadThemeSections(output string) []readSection {
	lines := strings.Split(output, "\n")
	var out []readSection
	var curPath string
	var body []string
	flush := func() {
		if curPath == "" && len(body) == 0 {
			return
		}
		out = append(out, readSection{path: curPath, body: strings.Join(body, "\n")})
		curPath, body = "", nil
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "### ") {
			flush()
			curPath = strings.TrimSpace(strings.TrimPrefix(line, "### "))
			continue
		}
		body = append(body, line)
	}
	flush()
	return out
}

func excerptSingleBody(path, body string) string {
	// Error / missing stubs are tiny — leave untouched.
	trim := strings.TrimSpace(body)
	if strings.HasPrefix(trim, "ERROR:") || trim == "(does not exist yet)" ||
		strings.HasPrefix(trim, "(omitted") || strings.HasPrefix(trim, "(remaining files omitted") {
		return body
	}
	lines := strings.Split(body, "\n")
	// Drop a single trailing empty line from Split for counting.
	n := len(lines)
	if n > 0 && lines[n-1] == "" {
		n--
		lines = lines[:n]
	}
	if n <= maxInteractiveReadLines {
		return body
	}

	headN := readExcerptHeadLines
	tailN := readExcerptTailLines
	if headN+tailN >= n {
		return body
	}
	omitted := n - headN - tailN
	outline := structuralOutline(lines, maxOutlineLines)

	var b strings.Builder
	if path != "" {
		fmt.Fprintf(&b, "(excerpt — %s is %d lines; showing outline + first %d + last %d. "+
			"Omitted %d middle lines. Re-read with a narrower focus or use grep_theme if you need a specific section.)\n\n",
			path, n, headN, tailN, omitted)
	} else {
		fmt.Fprintf(&b, "(excerpt — %d lines total; middle omitted.)\n\n", n)
	}
	if outline != "" {
		b.WriteString("## Structural outline\n")
		b.WriteString(outline)
		b.WriteString("\n")
	}
	b.WriteString("## Start of file\n")
	b.WriteString(strings.Join(lines[:headN], "\n"))
	b.WriteString("\n\n…\n\n## End of file\n")
	b.WriteString(strings.Join(lines[n-tailN:], "\n"))
	b.WriteByte('\n')
	return b.String()
}

// structuralOutline picks up to max lines that look like top-level Liquid /
// HTML structure so the model can navigate without the full body.
func structuralOutline(lines []string, max int) string {
	var picked []string
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if isStructuralLine(trim) {
			picked = append(picked, fmt.Sprintf("L%d: %s", i+1, truncateRunes(trim, 120)))
			if len(picked) >= max {
				break
			}
		}
	}
	if len(picked) == 0 {
		return ""
	}
	return strings.Join(picked, "\n") + "\n"
}

func isStructuralLine(trim string) bool {
	switch {
	case strings.HasPrefix(trim, "{%"):
		return true
	case strings.HasPrefix(trim, "{{"):
		return strings.Contains(trim, "render") || strings.Contains(trim, "section")
	case strings.HasPrefix(trim, "<"):
		// Opening tags for major layout elements — not every <span>.
		lower := strings.ToLower(trim)
		for _, tag := range []string{"<!doctype", "<html", "<head", "<body", "<header", "<footer",
			"<nav", "<main", "<section", "<article", "<aside", "<form", "<div class", "<div id",
			"<ul", "<ol", "<table", "<style", "<script", "<link", "<template"} {
			if strings.HasPrefix(lower, tag) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

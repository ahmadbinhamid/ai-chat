package themecheck

import (
	"log/slog"
	"strings"
)

// DowngradePreExistingFindings downgrades an error to a warning when the violating line, trimmed, already existed verbatim in baseline — matched by
// content not line number since an inserted line shifts all following numbers; biased toward "pre-existing" since a false "new" triggers an unwarranted repair round-trip.
func DowngradePreExistingFindings(findings []Finding, proposal Proposal, baseline map[string]string) []Finding {
	out := make([]Finding, len(findings))
	copy(out, findings)

	baselineLines := make(map[string]map[string]bool) // path -> set of trimmed baseline lines, computed lazily/once per path

	for i, f := range out {
		if f.Severity != SeverityError || f.Line <= 0 || f.Path == "" {
			continue
		}
		baseContent, hasBaseline := baseline[f.Path]
		if !hasBaseline {
			continue // brand-new file (or the fetch failed) — stay strict
		}
		pf, ok := proposal.fileByPath(f.Path)
		if !ok {
			continue // defensive: every finding's Path comes from a proposed file
		}
		violatingLine := strings.TrimSpace(lineTextAt(pf.Content, f.Line))
		if violatingLine == "" {
			continue // nothing meaningful to match — stay strict rather than match on blank lines
		}

		lines, ok := baselineLines[f.Path]
		if !ok {
			lines = trimmedLineSet(baseContent)
			baselineLines[f.Path] = lines
		}
		if lines[violatingLine] {
			out[i].Severity = SeverityWarning
			slog.Info("themecheck: downgraded pre-existing violation", "path", f.Path, "rule", f.Rule)
		}
	}

	return out
}

// lineTextAt returns line n (1-based) of content, or "" if n is out of range.
func lineTextAt(content string, n int) string {
	if n < 1 {
		return ""
	}
	lines := strings.Split(content, "\n")
	if n > len(lines) {
		return ""
	}
	return lines[n-1]
}

// trimmedLineSet returns the set of content's lines, each trimmed of leading/trailing whitespace.
func trimmedLineSet(content string) map[string]bool {
	lines := strings.Split(content, "\n")
	set := make(map[string]bool, len(lines))
	for _, line := range lines {
		set[strings.TrimSpace(line)] = true
	}
	return set
}

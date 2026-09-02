package themecheck

import "strings"

// DowngradePreExistingFindings downgrades an error-severity Finding to
// SeverityWarning when the violating line was already present in the file
// before this proposal touched it — i.e. the merchant's own theme already
// broke the rule, the model didn't introduce it. Everything else (warnings,
// theme-wide findings, findings a rule couldn't pin to a line, findings in a
// brand-new file) passes through unchanged. Findings are never dropped —
// only downgraded — so a real pre-existing problem still reaches the
// merchant via appendWarningsNote instead of being silently swallowed.
//
// baseline is the pre-change content of every file the proposal is
// updating, keyed by theme-relative path — a file the proposal is creating
// has no entry, which is exactly what marks it as having "no baseline"
// below. The caller decides how to source this (see themebuild.Service's
// buildSnapshot, which already fetches it into Snapshot.Files for every
// "update" proposal file); this package only consumes the map.
//
// A finding is treated as pre-existing when ALL of:
//   - f.Line > 0 (a rule that can't say where the problem is can't say it
//     was already there)
//   - the file has a baseline entry (a brand-new file is entirely the
//     model's responsibility)
//   - the violating line's content, trimmed, appears verbatim somewhere in
//     the baseline file's lines
//
// Matching is on LINE CONTENT, not line number, deliberately: if the model
// inserts even one line above the violation, every subsequent line number
// shifts, and index-based comparison would flag every remaining line as
// "changed" even though nothing about it actually did. Content matching is
// immune to that shift and needs no diff algorithm — a line either still
// exists in the file, verbatim, or it doesn't.
//
// This is deliberately approximate — it will misclassify a violating line
// the model duplicated or moved elsewhere in the file as "pre-existing"
// when it's really new (or vice versa, for a coincidentally identical
// line). That trade-off is intentional and biased toward "pre-existing" on
// purpose: a false "pre-existing" costs one unfixed violation surfaced as a
// warning the merchant can still see; a false "new" sends a perfectly fine
// proposal into a repair round-trip whose only way to "fix" an error it
// didn't cause is to delete content it never should have touched (see the
// Trustpilot-widget incident this exists to prevent). Given that asymmetry,
// erring toward "pre-existing" is the safer default even though it's not a
// real diff.
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
		}
	}

	return out
}

// lineTextAt returns line n (1-based) of content, or "" if n is out of range.
// Named distinctly from lineAt (offset -> line number) — this goes the
// other way, line number -> that line's text.
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

// trimmedLineSet returns the set of content's lines, each trimmed of
// leading/trailing whitespace — the shape DowngradePreExistingFindings
// matches a violating line against.
func trimmedLineSet(content string) map[string]bool {
	lines := strings.Split(content, "\n")
	set := make(map[string]bool, len(lines))
	for _, line := range lines {
		set[strings.TrimSpace(line)] = true
	}
	return set
}

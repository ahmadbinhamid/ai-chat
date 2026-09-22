package ai

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
)

// FileReader reads one theme file's current raw content by path, for materializing a
// GeneratedFile's "edit" action. An empty, non-error return means the path doesn't exist.
type FileReader func(ctx context.Context, path string) (content string, err error)

// maxEditMaterializationFailures bounds how many times a failure can repeat before warning
// the model plainly, rather than looping indefinitely on something it can't get right.
const maxEditMaterializationFailures = 2

// duplicatePathsFailureKey strike-counts the duplicate-paths failure, which isn't about
// any single file. Angle brackets never appear in a real theme-relative path, so no collision.
const duplicatePathsFailureKey = "<duplicate-paths>"

// materializeEdits turns every "edit"-action file into "update" with real content, in place.
// ok is false when a file failed to materialize; retryMessage lets the model self-correct
// rather than failing a generation that can cost minutes of streaming. failureCounts must
// persist across the whole Generate call, not reset per attempt.
func materializeEdits(ctx context.Context, result *Result, readFile FileReader, failureCounts map[string]int) (ok bool, retryMessage string) {
	if dupes := duplicateFilePaths(result.Files); len(dupes) > 0 {
		failureCounts[duplicatePathsFailureKey]++
		msg := fmt.Sprintf(
			"files[] proposes the same path more than once, which is ambiguous: %s. Each path must appear at most "+
				"once — combine every change to one file into a single files[] entry (multiple edits[] pairs on one "+
				"entry are fine).", strings.Join(dupes, ", "))
		if failureCounts[duplicatePathsFailureKey] >= maxEditMaterializationFailures {
			msg += " This has now failed repeatedly — repeating it again will fail the generation."
		}
		return false, msg
	}

	var problems []string
	for i := range result.Files {
		f := &result.Files[i]
		if f.Action != "edit" {
			continue
		}

		if len(f.Edits) == 0 {
			problems = append(problems, fmt.Sprintf(`%s: action "edit" requires at least one edits[] pair`, f.Path))
			continue
		}

		content, err := readFile(ctx, f.Path)
		if err != nil {
			failureCounts[f.Path]++
			slog.Warn("ai: edit materialization failed", "path", f.Path, "reason", "read_error", "failure_count", failureCounts[f.Path])
			problems = append(problems, fmt.Sprintf("%s: could not read current content to apply edits (%s) — try again", f.Path, err))
			continue
		}
		if content == "" {
			// An edit target must already exist. Still needs a hard stop after
			// maxEditMaterializationFailures like any other materialization failure.
			failureCounts[f.Path]++
			slog.Warn("ai: edit materialization failed", "path", f.Path, "reason", "file_not_found", "failure_count", failureCounts[f.Path])
			msg := fmt.Sprintf(
				`%s: does not exist — use action "create" with full content instead of "edit" for a new file`, f.Path)
			if failureCounts[f.Path] >= maxEditMaterializationFailures {
				msg += " This has now failed repeatedly — resubmitting \"edit\" for this path again will fail the generation."
			}
			problems = append(problems, msg)
			continue
		}

		newContent, tier, matchCount, applyErr := applyEdits(content, f.Edits)
		if applyErr != nil {
			failureCounts[f.Path]++
			slog.Warn("ai: edit materialization failed", "path", f.Path, "reason", "no_match", "tier", tier.String(),
				"match_count", matchCount, "failure_count", failureCounts[f.Path])
			if failureCounts[f.Path] >= maxEditMaterializationFailures {
				problems = append(problems, fmt.Sprintf(
					`%s: edits failed to apply %d times — resubmit this file with action "update" and its complete `+
						`corrected content instead of another edit attempt`, f.Path, failureCounts[f.Path]))
			} else {
				// Give the model the content it needs to self-correct without a tool call —
				// "try again" with no content just drives it to re-read/re-grep instead.
				problems = append(problems, noMatchProblem(f.Path, content, f.Edits, applyErr))
			}
			continue
		}

		slog.Info("ai: edit materialization succeeded", "path", f.Path, "tier", tier.String())
		// Captured before Action is overwritten; recapAssistantTurn needs the original value.
		f.OriginalAction = "edit"
		f.Action = "update"
		f.Content = newContent
		f.Edits = nil
	}

	if len(problems) == 0 {
		return true, ""
	}
	var b strings.Builder
	b.WriteString("Some proposed edits could not be applied — nothing was written yet. Fix these and call propose_changes again:\n\n")
	for _, p := range problems {
		fmt.Fprintf(&b, "- %s\n", p)
	}
	// This is Generate's own retry loop for a materialization failure, distinct from
	// repairPrompt's themecheck-rejection retry. Said once here, not once per problem.
	b.WriteString("\nDo not explore, read, or touch anything else — everything needed to fix the edit(s) above " +
		"is already here, or in your own last proposal already in this conversation. Fix them and call " +
		"propose_changes again.")
	return false, b.String()
}

// noMatchContentCap bounds how much of a file's content is inlined into a no_match retry
// message — covers most theme files whole, while keeping several simultaneous failures bounded.
const noMatchContentCap = 8_000

// noMatchWindowBytes is the near-miss window size when a file exceeds noMatchContentCap,
// half before/after the anchor — enough to show surrounding markup, cheap even at scale.
const noMatchWindowBytes = 4_000

// noMatchProblem builds the retry message for one no_match failure: full content if it fits,
// a bounded near-miss window if not, or a "re-read this file" instruction otherwise — avoids
// driving the model to expensively re-read/re-grep the file itself.
func noMatchProblem(path, content string, edits []Edit, applyErr error) string {
	base := fmt.Sprintf("%s: %s", path, applyErr)
	if len(content) <= noMatchContentCap {
		return fmt.Sprintf("%s\n\n%s's real current content, to find the exact text to match:\n\n%s", base, path, content)
	}
	if window, ok := nearMissWindow(content, edits, noMatchWindowBytes); ok {
		return fmt.Sprintf(
			"%s\n\n%s is %d bytes, too large to inline in full — here is the content around where this text "+
				"looks closest:\n\n%s", base, path, len(content), window)
	}
	return fmt.Sprintf(
		"%s\n\n%s is %d bytes, too large to inline, and no close match was found nearby — re-read this one "+
			"file specifically (not the rest of the theme) before trying again.", base, path, len(content))
}

// nearMissWindow locates a plausible anchor cheaply via plain substring search for each
// edit's first line, returning up to windowBytes centered on the first match found.
func nearMissWindow(content string, edits []Edit, windowBytes int) (window string, found bool) {
	for _, e := range edits {
		anchor := firstLine(e.OldString)
		if strings.TrimSpace(anchor) == "" {
			continue
		}
		idx := strings.Index(content, anchor)
		if idx < 0 {
			continue
		}
		half := windowBytes / 2
		start := idx - half
		if start < 0 {
			start = 0
		}
		end := idx + len(anchor) + half
		if end > len(content) {
			end = len(content)
		}
		return content[start:end], true
	}
	return "", false
}

// matchTier identifies which matching strategy resolved an edit, increasing tolerance order.
// Logged (never old_string or file content) so a failure says which tier the file needed.
type matchTier int

const (
	tierNone matchTier = iota
	tierExact
	tierTrimmed
	tierCollapsed
)

func (t matchTier) String() string {
	switch t {
	case tierExact:
		return "exact"
	case tierTrimmed:
		return "trimmed"
	case tierCollapsed:
		return "collapsed"
	default:
		return "none"
	}
}

// applyEdits applies edits in order, each old_string located against content AS IT STANDS
// after every prior edit — so overlapping edits naturally fail uniqueness with no special
// handling. worstTier is the loosest tier any edit needed, for materializeEdits to log.
func applyEdits(content string, edits []Edit) (result string, worstTier matchTier, matchCount int, err error) {
	worstTier = tierExact
	for i, e := range edits {
		start, end, tier, count, matchErr := findMatch(content, e.OldString)
		if matchErr != nil {
			return "", tierNone, count, fmt.Errorf("edit %d: %w", i+1, matchErr)
		}
		if tier > worstTier {
			worstTier = tier
		}

		replacement := e.NewString
		if tier != tierExact && replacement != "" {
			// Tiers 2/3 matched despite whitespace drift, so the matched region's real
			// indentation (which the replacement is about to discard) must be reapplied.
			replacement = reindentToMatch(content[start:end], replacement)
		}
		content = content[:start] + replacement + content[end:]
	}
	return content, worstTier, 0, nil
}

// findMatch locates old_string in content, trying tiers in order and stopping at the first
// that resolves to exactly one location: 1) exact byte-for-byte, 2) per-line trimmed,
// 3) whitespace-collapsed (catches `class="a  b"` vs `class="a b"`).
// A tier with zero matches falls through; a tier with more than one is an immediate failure,
// never falling through to a looser tier that could pick blind. start/end are always byte
// offsets into the ORIGINAL content, never the whitespace-normalized comparison copies.
func findMatch(content, oldString string) (start, end int, tier matchTier, matchCount int, err error) {
	switch count := strings.Count(content, oldString); count {
	case 1:
		i := strings.Index(content, oldString)
		return i, i + len(oldString), tierExact, 1, nil
	case 0:
		// fall through to tiers 2/3 below
	default:
		return 0, 0, tierNone, count, fmt.Errorf("old_string matched %d times, must match exactly once — add more surrounding context to make it unique", count)
	}

	// A whitespace-only old_string would trivially "match" every blank line under trimmed
	// comparison; only exact matching (already tried above) applies to it.
	if strings.TrimSpace(oldString) == "" {
		return 0, 0, tierNone, 0, fmt.Errorf("old_string not found (0 matches)")
	}

	// A trailing "\n" in old_string terminates its last line rather than declaring an extra
	// blank line — strings.Split would otherwise produce a synthetic empty final element.
	oldLines := strings.Split(strings.TrimSuffix(oldString, "\n"), "\n")
	contentLines := splitContentLines(content)

	for _, tier := range []struct {
		id     matchTier
		normal func(string) string
	}{
		{tierTrimmed, strings.TrimSpace},
		{tierCollapsed, collapseTrimmed},
	} {
		matches := findLineWindows(contentLines, oldLines, tier.normal)
		switch len(matches) {
		case 0:
			continue
		case 1:
			i := matches[0]
			// end is BEFORE the last matched line's trailing '\n', so the line ending
			// between the matched region and the rest of the file stays untouched.
			return contentLines[i].start, contentLines[i+len(oldLines)-1].end, tier.id, 1, nil
		default:
			return 0, 0, tierNone, len(matches), fmt.Errorf("old_string matched %d locations, must match exactly one — add more surrounding context to make it unique", len(matches))
		}
	}

	return 0, 0, tierNone, 0, fmt.Errorf("old_string not found (0 matches)")
}

// whitespaceRunRe matches a run of spaces/tabs, never newlines (already the line boundary).
var whitespaceRunRe = regexp.MustCompile(`[ \t]+`)

func collapseTrimmed(s string) string {
	return whitespaceRunRe.ReplaceAllString(strings.TrimSpace(s), " ")
}

// lineOffset is one line with byte offsets back into the ORIGINAL content string. [start, end)
// excludes both the line's '\n' and any trailing '\r' — a CRLF's '\r' belongs to the line
// ending, not the content, since end is also the splice boundary for a matched region.
type lineOffset struct {
	text       string
	start, end int
}

// newLineOffset builds one lineOffset for content[start:end), trimming a trailing '\r'
// from the boundary itself (not just the text) if content uses CRLF at this line.
func newLineOffset(content string, start, end int) lineOffset {
	if end > start && content[end-1] == '\r' {
		end--
	}
	return lineOffset{text: content[start:end], start: start, end: end}
}

// splitContentLines splits content into lines with byte offsets valid to slice content
// directly. Content with no trailing newline still yields a final line.
func splitContentLines(content string) []lineOffset {
	var lines []lineOffset
	start := 0
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			lines = append(lines, newLineOffset(content, start, i))
			start = i + 1
		}
	}
	lines = append(lines, newLineOffset(content, start, len(content)))
	return lines
}

// findLineWindows returns the starting index of every contiguous window of contentLines
// matching oldLines under normal, applied to both sides independently.
func findLineWindows(contentLines []lineOffset, oldLines []string, normal func(string) string) []int {
	normOld := make([]string, len(oldLines))
	for i, l := range oldLines {
		normOld[i] = normal(l)
	}

	var matches []int
	for i := 0; i+len(oldLines) <= len(contentLines); i++ {
		match := true
		for k, want := range normOld {
			if normal(contentLines[i+k].text) != want {
				match = false
				break
			}
		}
		if match {
			matches = append(matches, i)
		}
	}
	return matches
}

// reindentTabWidth is the fixed column width a '\t' counts as when measuring indentation for
// reindentToMatch's delta — a consistent width to compute from, not real tab-stop rendering.
const reindentTabWidth = 4

// reindentToMatch re-indents inserted (new_string) to sit at matched's leading indentation
// (tier 2/3 only), since the replacement entirely discards matched's own leading whitespace.
//
// Applies a signed DELTA (target width minus inserted's first-line width) to every line's
// existing indentation, clamped at zero — so a shallower line (e.g. a closing-tag cascade
// dedenting back out) keeps its own smaller depth instead of being flattened to one uniform
// indentation. Blank lines stay blank. Output indentation is always plain spaces, never tabs —
// a tab only counts as reindentTabWidth columns for the delta arithmetic; emitting that many
// literal tab characters back out would be a 4x blowup on a tab-indented target.
func reindentToMatch(matched, inserted string) string {
	targetWidth := indentWidth(leadingWhitespace(firstLine(matched)))
	insertedLines := strings.Split(inserted, "\n")
	delta := targetWidth - indentWidth(leadingWhitespace(insertedLines[0]))

	for i, line := range insertedLines {
		if strings.TrimSpace(line) == "" {
			insertedLines[i] = ""
			continue
		}
		lineIndent := leadingWhitespace(line)
		newWidth := indentWidth(lineIndent) + delta
		if newWidth < 0 {
			newWidth = 0
		}
		insertedLines[i] = strings.Repeat(" ", newWidth) + line[len(lineIndent):]
	}
	return strings.Join(insertedLines, "\n")
}

// indentWidth measures s (pure leading whitespace, via leadingWhitespace) in columns.
func indentWidth(s string) int {
	width := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\t' {
			width += reindentTabWidth
		} else {
			width++
		}
	}
	return width
}

func leadingWhitespace(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// duplicateFilePaths returns every path appearing more than once in files, sorted for a
// deterministic message — proposing the same path twice is always ambiguous.
func duplicateFilePaths(files []GeneratedFile) []string {
	seen := make(map[string]int, len(files))
	for _, f := range files {
		seen[f.Path]++
	}
	var dupes []string
	for path, count := range seen {
		if count > 1 {
			dupes = append(dupes, path)
		}
	}
	sort.Strings(dupes)
	return dupes
}

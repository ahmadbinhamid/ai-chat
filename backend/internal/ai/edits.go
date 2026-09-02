package ai

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
)

// FileReader reads one theme file's current raw content by path — used only
// to materialize a GeneratedFile's "edit" action into full content (see
// materializeEdits). Distinct from ToolExecutor: that's scoped to
// model-invoked tool calls and returns a human-formatted string for the
// model to read; this needs one file's exact raw bytes and a clean error,
// never model-facing text. An empty, non-error return means the path
// doesn't exist — the same convention themefs.ThemeStore.ReadFile already
// uses (a 404 is not an error), so a themebuild-supplied FileReader is
// just that method with auth curried in, nothing extra to implement.
type FileReader func(ctx context.Context, path string) (content string, err error)

// maxEditMaterializationFailures bounds how many times ONE file's edits are
// allowed to fail materialization (a bad old_string, not a read error)
// before materializeEdits stops asking for a corrected edit and tells the
// model to resubmit that file as a full "update" instead — see
// materializeEdits' own doc comment on why this is the safe fallback rather
// than looping indefinitely on a find/replace the model can't get right.
const maxEditMaterializationFailures = 2

// materializeEdits turns every "edit"-action file in result into "update"
// with real content, in place — see GeneratedFile's own doc comment for why
// this makes "edit" a wire-format optimization only, invisible to every
// caller downstream of Generate. ok is false when at least one file failed
// to materialize (a duplicate path, a missing edits[] entry, an old_string
// that matched zero or several times at every tier, a read failure);
// retryMessage then describes every failure so Generate's caller can feed
// it back as a tool_result and let the model correct itself on the next
// iteration, rather than failing the generation over what's usually a
// one-line mistake — this matters a lot more than it sounds: a
// propose_changes call can take minutes to stream, so a rejected
// materialization throws away that whole cost, not just a cheap round trip.
// failureCounts is keyed by path and must persist across the whole Generate
// call (not be reset per attempt) — see the constant above.
func materializeEdits(ctx context.Context, result *Result, readFile FileReader, failureCounts map[string]int) (ok bool, retryMessage string) {
	if dupes := duplicateFilePaths(result.Files); len(dupes) > 0 {
		return false, fmt.Sprintf(
			"files[] proposes the same path more than once, which is ambiguous: %s. Each path must appear at most "+
				"once — combine every change to one file into a single files[] entry (multiple edits[] pairs on one "+
				"entry are fine).", strings.Join(dupes, ", "))
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
			// readFile returns "" for a path that doesn't exist (see
			// FileReader's doc comment) — an edit target must already
			// exist, that's the whole premise of a find/replace. Not
			// counted toward failureCounts: falling back to "update" isn't
			// a fix here, "create" already is.
			slog.Warn("ai: edit materialization failed", "path", f.Path, "reason", "file_not_found")
			problems = append(problems, fmt.Sprintf(
				`%s: does not exist — use action "create" with full content instead of "edit" for a new file`, f.Path))
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
				problems = append(problems, fmt.Sprintf("%s: %s", f.Path, applyErr))
			}
			continue
		}

		slog.Info("ai: edit materialization succeeded", "path", f.Path, "tier", tier.String())
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
	return false, b.String()
}

// matchTier identifies which matching strategy resolved an edit, in
// increasing order of tolerance — see findMatch. Logged (never old_string
// or file content itself) so a real-world materialization failure says
// which tier the file needed, or that none worked.
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

// applyEdits applies edits to content in order, each edit's old_string
// located via findMatch against content AS IT STANDS after every prior
// edit in the list has already been applied — so two edits that overlap or
// target the same text simply have the second one fail its own uniqueness
// check naturally, with no special handling, and the tiers are always run
// against genuinely current content rather than offsets pre-computed
// against the original. worstTier is the loosest tier any single edit in
// the list needed (tierExact if every one matched byte-for-byte) — the
// summary materializeEdits logs for the whole file.
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
			// Tier 1 (exact) means old_string was already byte-identical to
			// the file, including its indentation — new_string, written to
			// align with what the model read, needs no adjustment. Tiers 2
			// and 3 matched despite whitespace drift, so the matched
			// region's real indentation (which the replacement is about to
			// discard, since the whole region including its own leading
			// whitespace is being replaced) has to be reapplied — see
			// reindentToMatch.
			replacement = reindentToMatch(content[start:end], replacement)
		}
		content = content[:start] + replacement + content[end:]
	}
	return content, worstTier, 0, nil
}

// findMatch locates old_string in content, trying tiers in order and
// stopping at the first that resolves to exactly one location:
//
//  1. Exact — byte-for-byte substring match. Unchanged from before tiered
//     matching existed; this is the fast path and must stay first.
//  2. Per-line trimmed — old_string's lines against a same-length window of
//     content's real lines, each side stripped of leading/trailing
//     whitespace before comparing.
//  3. Whitespace-collapsed — as tier 2, but internal runs of spaces/tabs are
//     also collapsed to one space on both sides first. Catches
//     `class="a  b"` vs `class="a b"` and similar reflow.
//
// A tier producing zero matches falls through to the next; a tier
// producing more than one is an immediate failure — never picked, never
// falls through to a looser tier (a looser tier finding a unique answer
// where a stricter one found two would be picking blind). matchCount is 0
// or the actual (>1) count on failure, for structured logging — callers
// must never parse it back out of err's text.
//
// start/end are always byte offsets into the ORIGINAL, unmodified content:
// tiers 2/3 compare whitespace-normalized copies of each candidate line,
// but the returned range is read from the real, unnormalized line
// boundaries (see splitContentLines) — never from the normalized strings,
// which have a different length whenever anything actually needed
// normalizing.
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

	// A whitespace-only old_string would trivially "match" every blank line
	// under trimmed comparison — not a real anchor. Only exact matching
	// (already tried above) applies to it.
	if strings.TrimSpace(oldString) == "" {
		return 0, 0, tierNone, 0, fmt.Errorf("old_string not found (0 matches)")
	}

	// A trailing "\n" in old_string terminates its last real line rather
	// than declaring an extra blank line after it — strings.Split would
	// otherwise produce a synthetic empty final element that forces a
	// nonexistent blank line into the match window. Trimming it once here
	// (not repeatedly — a second, intentional trailing blank line stays
	// represented by its own empty element) is the whole fix; the
	// newline's presence in the file is preserved separately, by end never
	// extending past the matched lines' own text (see below).
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
			// end is the last matched line's own text end — BEFORE its
			// trailing '\n', not after. content[end:] then still starts
			// with that '\n' (or whatever followed), so the line ending
			// between the matched region and the rest of the file is
			// preserved untouched regardless of how old_string itself was
			// terminated — simpler and just as correct as tracking
			// old_string's own trailing-newline intent separately.
			return contentLines[i].start, contentLines[i+len(oldLines)-1].end, tier.id, 1, nil
		default:
			return 0, 0, tierNone, len(matches), fmt.Errorf("old_string matched %d locations, must match exactly one — add more surrounding context to make it unique", len(matches))
		}
	}

	return 0, 0, tierNone, 0, fmt.Errorf("old_string not found (0 matches)")
}

// whitespaceRunRe matches a run of spaces/tabs — never newlines, which
// splitContentLines/strings.Split already use as the line boundary.
var whitespaceRunRe = regexp.MustCompile(`[ \t]+`)

func collapseTrimmed(s string) string {
	return whitespaceRunRe.ReplaceAllString(strings.TrimSpace(s), " ")
}

// lineOffset is one line of some content string, with byte offsets back
// into that ORIGINAL string — [start, end) selects exactly text, which
// never includes the line's own '\n' AND never includes a trailing '\r'
// either (a CRLF-terminated line's '\r' belongs to the line ending, not the
// line's content — see newLineOffset). That matters for more than
// comparison: end is also the splice boundary a matched multi-line region
// is cut at, and a '\r' left inside that boundary would get silently eaten
// by the replacement instead of surviving as part of the line ending that
// follows it untouched.
type lineOffset struct {
	text       string
	start, end int
}

// newLineOffset builds one lineOffset for content[start:end), first
// trimming a trailing '\r' from the boundary itself (not just from text)
// if content uses CRLF at this line — see lineOffset's own doc comment for
// why the boundary, not just the text, has to exclude it.
func newLineOffset(content string, start, end int) lineOffset {
	if end > start && content[end-1] == '\r' {
		end--
	}
	return lineOffset{text: content[start:end], start: start, end: end}
}

// splitContentLines splits content into lines with byte offsets, without
// copying or modifying content itself — every offset returned is valid to
// slice content directly. Content with no trailing newline still yields a
// final line for whatever follows the last '\n' (or the whole string, if
// there's no '\n' at all).
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

// findLineWindows returns the starting content-line index of every place a
// same-length, contiguous window of contentLines matches oldLines under
// normal (applied to both sides independently, never compared cross-tier).
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

// reindentTabWidth is the fixed column width a '\t' counts as when
// measuring indentation for reindentToMatch's delta — not an attempt to
// match any real editor's tab-stop rendering, just a consistent,
// deterministic width to compute a signed delta from. Theme files are
// near-universally space-indented in practice; this only matters at all on
// the rare file that mixes tabs in.
const reindentTabWidth = 4

// reindentToMatch re-indents inserted (new_string) to sit at matched's
// (the real file region old_string resolved to, tier 2/3 only) leading
// indentation. The replacement text entirely replaces matched, including
// matched's own leading whitespace — so unless inserted supplies the right
// indentation itself, the file loses it at the edit point.
//
// Rule applied: a signed DELTA, not a prefix strip. delta = target's
// leading-whitespace width minus inserted's own first line's
// leading-whitespace width (widths measured with reindentTabWidth). That
// same delta is added to every line's own existing indentation width,
// clamped at zero. A line indented deeper than the first keeps its extra
// depth on top of the shift; a line indented SHALLOWER than the first — a
// closing-tag cascade dedenting back out through several nested elements,
// completely normal in Liquid/HTML — keeps its own smaller depth too,
// rather than being flattened to one uniform indentation (a real bug an
// earlier strip-and-prepend version of this rule had: any line that didn't
// start with the first line's exact indentation prefix fell back to
// discarding its indentation entirely). A genuinely blank line is left
// blank, never given trailing whitespace it didn't have.
//
// Mixed tabs/spaces: every output line's new indentation is rebuilt from
// the TARGET's own leading-whitespace character (its first character, or a
// space if the target has none) repeated to the computed width — never a
// mix of a line's original character and the target's. Re-indenting is
// already recomputing a width from scratch, not shifting existing
// characters in place, so there's no natural "original character" to
// preserve; using the target's keeps the re-indented block visually
// consistent with what actually surrounds it in the file.
func reindentToMatch(matched, inserted string) string {
	targetIndent := leadingWhitespace(firstLine(matched))
	targetWidth := indentWidth(targetIndent)
	targetChar := byte(' ')
	if targetIndent != "" {
		targetChar = targetIndent[0]
	}

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
		insertedLines[i] = strings.Repeat(string(targetChar), newWidth) + line[len(lineIndent):]
	}
	return strings.Join(insertedLines, "\n")
}

// indentWidth measures s — expected to already be pure leading whitespace,
// via leadingWhitespace — in columns: a tab counts as reindentTabWidth,
// everything else (a space) counts as 1.
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

// duplicateFilePaths returns every path that appears more than once in
// files, sorted for a deterministic message — proposing the same path twice
// is ambiguous regardless of which actions are involved (edit+update on the
// same path is the case that motivated this, but two "edit" entries for one
// path, or two "update" entries, are exactly as undefined).
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

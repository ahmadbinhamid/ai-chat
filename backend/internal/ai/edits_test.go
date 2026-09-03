package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestApplyEdits_UniqueMatchReplaces(t *testing.T) {
	got, tier, _, err := applyEdits("line one\nline two\nline three\n", []Edit{{OldString: "line two", NewString: "LINE TWO"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "line one\nLINE TWO\nline three\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if tier != tierExact {
		t.Errorf("expected a byte-exact match to resolve at tier %s, got %s — a regression here would silently promote everything to a looser tier", tierExact, tier)
	}
}

func TestApplyEdits_ZeroMatchesErrors(t *testing.T) {
	_, _, count, err := applyEdits("line one\nline two\n", []Edit{{OldString: "line nope", NewString: "x"}})
	if err == nil {
		t.Fatal("expected an error for a zero-match old_string")
	}
	if count != 0 {
		t.Errorf("expected matchCount 0, got %d", count)
	}
}

func TestApplyEdits_MultipleMatchesErrors(t *testing.T) {
	_, _, count, err := applyEdits("dup\ndup\n", []Edit{{OldString: "dup", NewString: "x"}})
	if err == nil {
		t.Fatal("expected an error for an old_string that matches more than once")
	}
	if count != 2 {
		t.Errorf("expected matchCount 2, got %d", count)
	}
}

func TestApplyEdits_EmptyNewStringDeletes(t *testing.T) {
	got, _, _, err := applyEdits("keep\nremove me\nkeep too\n", []Edit{{OldString: "remove me\n", NewString: ""}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "keep\nkeep too\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestApplyEdits_AppliedInOrderOverlapFailsNaturally(t *testing.T) {
	// Two edits both target "target" — the first consumes it, so the second
	// (now searching post-first-edit content, which no longer contains
	// "target") fails its own zero-match check. No special-casing needed.
	_, _, _, err := applyEdits("target\n", []Edit{
		{OldString: "target", NewString: "first"},
		{OldString: "target", NewString: "second"},
	})
	if err == nil {
		t.Fatal("expected the second overlapping edit to fail its own uniqueness check")
	}
}

// TestApplyEdits_Tier2MatchesWrongIndentation covers the primary reason
// tiered matching exists: the model reproduces a line's real text but not
// its exact indentation (having read it many iterations earlier). old_string
// here is indented with 2 spaces; the file actually has 4. Tier 2 must
// still find it, and the replacement must come out at the FILE's real
// indentation, not old_string's.
func TestApplyEdits_Tier2MatchesWrongIndentation(t *testing.T) {
	// old_string uses MORE leading spaces than the file's real 4 — deliberately
	// not just fewer, since "  <li>old</li>" (2 spaces) would still be a
	// literal byte substring of "    <li>old</li>" (4 spaces), letting tier 1
	// match it by accident and defeating the point of this test.
	content := "<ul>\n    <li>keep</li>\n    <li>old</li>\n</ul>\n"
	got, tier, _, err := applyEdits(content, []Edit{{OldString: "      <li>old</li>", NewString: "      <li>new</li>"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier != tierTrimmed {
		t.Fatalf("expected tier %s, got %s", tierTrimmed, tier)
	}
	want := "<ul>\n    <li>keep</li>\n    <li>new</li>\n</ul>\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestApplyEdits_Tier3MatchesCollapsedSpacing covers internal whitespace
// reflow (e.g. class="a  b" vs "a b") that tier 2's per-line trim alone
// doesn't fix.
func TestApplyEdits_Tier3MatchesCollapsedSpacing(t *testing.T) {
	content := `<div class="a  b  c">text</div>` + "\n"
	got, tier, _, err := applyEdits(content, []Edit{
		{OldString: `<div class="a b c">text</div>`, NewString: `<div class="a b c">TEXT</div>`},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier != tierCollapsed {
		t.Fatalf("expected tier %s, got %s", tierCollapsed, tier)
	}
	want := `<div class="a b c">TEXT</div>` + "\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestApplyEdits_Tier2AmbiguousFailsRatherThanFallingThrough confirms the
// non-negotiable ambiguity rule: a tier-2 candidate appearing twice must
// fail outright, never fall through to tier 3 (which might resolve to one
// match) and never silently pick the first.
func TestApplyEdits_Tier2AmbiguousFailsRatherThanFallingThrough(t *testing.T) {
	// Both occurrences trim-match "<li>dup</li>" (one with 2-space, one with
	// 4-space indentation) — ambiguous at tier 2. Tier 3 would ALSO match
	// both (collapsing whitespace doesn't disambiguate two identical lines),
	// so this can't accidentally pass by falling through either.
	content := "<ul>\n  <li>dup</li>\n    <li>dup</li>\n</ul>\n"
	_, _, count, err := applyEdits(content, []Edit{{OldString: "<li>dup</li>", NewString: "<li>x</li>"}})
	if err == nil {
		t.Fatal("expected an error for a tier-2 candidate matching twice")
	}
	if count != 2 {
		t.Errorf("expected matchCount 2, got %d", count)
	}
}

// TestApplyEdits_AllThreeTiersZeroFails confirms a genuinely absent
// old_string fails cleanly (not a panic, not a false match) when none of
// the three tiers find anything at all.
func TestApplyEdits_AllThreeTiersZeroFails(t *testing.T) {
	_, tier, count, err := applyEdits("<p>hello</p>\n", []Edit{{OldString: "<p>goodbye</p>", NewString: "x"}})
	if err == nil {
		t.Fatal("expected an error when no tier finds a match")
	}
	if tier != tierNone {
		t.Errorf("expected tier %s on total failure, got %s", tierNone, tier)
	}
	if count != 0 {
		t.Errorf("expected matchCount 0, got %d", count)
	}
}

// TestApplyEdits_WhitespaceOnlyOldStringDoesNotMatchEveryBlankLine is the
// degenerate-input guard: a purely-whitespace old_string must not resolve
// against an arbitrary blank line under trimmed comparison (every blank
// line trims to "", so without this guard tier 2 would treat the file as
// having exactly one blank line whenever it happens to have exactly one).
func TestApplyEdits_WhitespaceOnlyOldStringDoesNotMatchEveryBlankLine(t *testing.T) {
	content := "line one\n\nline three\n" // exactly one blank line
	_, _, _, err := applyEdits(content, []Edit{{OldString: "   ", NewString: "x"}})
	if err == nil {
		t.Fatal("expected a whitespace-only old_string to fail rather than match the file's one blank line")
	}
}

// TestApplyEdits_ByteOffsetCorrectnessInLargeMixedIndentFile is the
// off-by-N regression guard the task specifically calls for: a tier-2 edit
// late in a file with mixed (2-space vs 4-space) indentation must splice
// at exactly the right place — nothing before or after the match may be
// truncated, duplicated, or shifted.
func TestApplyEdits_ByteOffsetCorrectnessInLargeMixedIndentFile(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "  <li>filler %d</li>\n", i)
	}
	before := b.String()
	target := "    <li>TARGET LINE</li>\n" // 4-space indent, the rest of the file uses 2
	var after strings.Builder
	for i := 200; i < 400; i++ {
		fmt.Fprintf(&after, "  <li>filler %d</li>\n", i)
	}
	content := before + target + after.String()

	// old_string uses MORE leading spaces (6) than the target line's real 4
	// — deliberately not fewer, since a shorter indent would still be a
	// literal byte substring of the real line and let tier 1 match by
	// accident (see TestApplyEdits_Tier2MatchesWrongIndentation).
	got, tier, _, err := applyEdits(content, []Edit{
		{OldString: "      <li>TARGET LINE</li>", NewString: "      <li>REPLACED</li>"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier != tierTrimmed {
		t.Fatalf("expected tier %s, got %s", tierTrimmed, tier)
	}
	want := before + "    <li>REPLACED</li>\n" + after.String()
	if got != want {
		// Diff by length/prefix rather than dumping the whole (large) file.
		minLen := len(got)
		if len(want) < minLen {
			minLen = len(want)
		}
		firstDiff := minLen
		for i := 0; i < minLen; i++ {
			if got[i] != want[i] {
				firstDiff = i
				break
			}
		}
		t.Fatalf("byte offset mismatch: got len %d, want len %d, first differing byte at %d (got %q, want %q)",
			len(got), len(want), firstDiff, snippet(got, firstDiff), snippet(want, firstDiff))
	}
}

func snippet(s string, at int) string {
	start := at - 20
	if start < 0 {
		start = 0
	}
	end := at + 20
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}

// TestReindentToMatch_PreservesRelativeNesting is the re-indentation rule
// spelled out: only the DELTA between inserted's own first line and each
// subsequent line is preserved on top of the file's real target
// indentation — a line nested one level deeper than new_string's first
// line stays one level deeper than the target, it isn't flattened to the
// target uniformly.
func TestReindentToMatch_PreservesRelativeNesting(t *testing.T) {
	// File's real indentation is 4 spaces; the model wrote new_string
	// against what it thought was 6-space indentation, with a nested child
	// one level (+2 spaces) deeper than its own first line, and a closing
	// tag realigned back to the first line's level — ordinary HTML nesting.
	content := "<div>\n  <ul>\n    <li>old</li>\n  </ul>\n</div>\n"
	newString := "      <li>new\n        <span>nested</span>\n      </li>"
	got, tier, _, err := applyEdits(content, []Edit{{OldString: "      <li>old</li>", NewString: newString}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier != tierTrimmed {
		t.Fatalf("expected tier %s, got %s", tierTrimmed, tier)
	}
	// First line lands at the file's real 4-space indentation; the nested
	// line keeps its own +2-space delta on top of that (6 spaces, not
	// flattened to 4), and the closing tag realigns back to 4 with the
	// opening tag, exactly as it was relative to it in new_string.
	want := "<div>\n  <ul>\n    <li>new\n      <span>nested</span>\n    </li>\n  </ul>\n</div>\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestReindentToMatch_ClosingCascade is the case that motivated the switch
// from strip-and-prepend to a signed delta: new_string dedents across
// several lines as it closes nested elements (8/6/4 spaces), completely
// normal Liquid/HTML. The old rule flattened every line but the first
// because only the first line's exact indentation prefix was recognized;
// the delta rule shifts every line by the same signed amount instead.
func TestReindentToMatch_ClosingCascade(t *testing.T) {
	matched := "    <div>old</div>"                                           // target: 4 spaces
	inserted := "        <p>Powered By FlowPOS</p>\n      </div>\n    </div>" // 8/6/4
	got := reindentToMatch(matched, inserted)
	want := "    <p>Powered By FlowPOS</p>\n  </div>\n</div>" // 4/2/0
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestReindentToMatch_DeeperNestingStillWorks is the pre-existing
// behaviour the delta rule must not regress: a child line indented deeper
// than new_string's first line keeps that extra depth on top of the shift.
func TestReindentToMatch_DeeperNestingStillWorks(t *testing.T) {
	matched := "    <li>old</li>"                                         // target: 4 spaces
	inserted := "      <li>new\n        <span>nested</span>\n      </li>" // 6/8/6
	got := reindentToMatch(matched, inserted)
	want := "    <li>new\n      <span>nested</span>\n    </li>" // 4/6/4
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestReindentToMatch_ClampsNegativeDeltaToZero confirms a delta steep
// enough to push a shallow line negative lands at column 0 rather than
// panicking (a negative strings.Repeat count) or producing a negative
// slice index.
func TestReindentToMatch_ClampsNegativeDeltaToZero(t *testing.T) {
	matched := "<div>old</div>"                             // target: 0 spaces
	inserted := "        <p>deep</p>\n    <p>less deep</p>" // delta = 0-8 = -8; both lines clamp to 0
	got := reindentToMatch(matched, inserted)
	want := "<p>deep</p>\n<p>less deep</p>"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestReindentToMatch_ZeroDeltaByteIdentical confirms that when the
// source and target indents already agree, the output is untouched.
func TestReindentToMatch_ZeroDeltaByteIdentical(t *testing.T) {
	matched := "    <div>old</div>" // target: 4 spaces, matching new_string's own first line
	inserted := "    <p>a</p>\n      <p>b</p>"
	got := reindentToMatch(matched, inserted)
	if got != inserted {
		t.Errorf("expected byte-identical output on zero delta, got %q, want %q", got, inserted)
	}
}

// TestReindentToMatch_BlankLinesStayBlank confirms a blank separator line
// inside a re-indented block is never given stray leading whitespace.
func TestReindentToMatch_BlankLinesStayBlank(t *testing.T) {
	matched := "    <div>old</div>"
	inserted := "        <p>a</p>\n\n        <p>b</p>"
	got := reindentToMatch(matched, inserted)
	want := "    <p>a</p>\n\n    <p>b</p>"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestReindentToMatch_TabTargetZeroDeltaEmitsSpacesNotFourTabs is the
// regression this task exists for: indentWidth measures a tab as
// reindentTabWidth (4) columns for the delta ARITHMETIC, but the old
// implementation then emitted that many literal TAB characters back out —
// one tab of real indentation becoming four tabs at zero delta. Output
// must be four columns of plain spaces, never repeated tab characters.
func TestReindentToMatch_TabTargetZeroDeltaEmitsSpacesNotFourTabs(t *testing.T) {
	matched := "\t<div>old</div>" // target: 1 tab = width 4
	inserted := "\t<p>new</p>"    // source: 1 tab = width 4, so delta = 0
	got := reindentToMatch(matched, inserted)
	want := "    <p>new</p>" // 4 spaces — NOT "\t\t\t\t"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestReindentToMatch_TabTargetClosingCascade is the tab-indented
// equivalent of the space-indented closing-cascade test: widths still
// shift by the same signed delta and relative structure is still
// preserved, just expressed in spaces on the way out.
func TestReindentToMatch_TabTargetClosingCascade(t *testing.T) {
	matched := "\t<div>old</div>" // target: 1 tab = width 4
	// source (new_string's first line): 2 tabs = width 8; delta = 4-8 = -4
	inserted := "\t\t<p>Powered By FlowPOS</p>\n\t</div>\n</div>"
	got := reindentToMatch(matched, inserted)
	want := "    <p>Powered By FlowPOS</p>\n</div>\n</div>" // widths 4, 0, 0 (clamped)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestReindentToMatch_MixedTabAndSpaceInsertedNormalizesToSpaces confirms
// a new_string that itself mixes tabs and spaces re-indents to plain
// spaces at the correct computed widths against a space-indented target.
func TestReindentToMatch_MixedTabAndSpaceInsertedNormalizesToSpaces(t *testing.T) {
	matched := "    <div>old</div>" // target: 4 spaces = width 4
	// line0 "\t  " = 1 tab (4) + 2 spaces = width 6; delta = 4-6 = -2
	// line1 "\t\t" = 2 tabs = width 8; 8-2 = 6
	inserted := "\t  <p>a</p>\n\t\t<p>b</p>"
	got := reindentToMatch(matched, inserted)
	want := "    <p>a</p>\n      <p>b</p>" // 4 spaces, then 6 spaces
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestApplyEdits_ExactTierNeverReindents confirms tier 1 splices new_string
// byte-for-byte with no re-indentation at all — new_string's deliberately
// "wrong" 2-space indent (the file elsewhere uses 4) must survive
// untouched, since an exact old_string match means the model already
// copied the file's real current indentation verbatim.
func TestApplyEdits_ExactTierNeverReindents(t *testing.T) {
	content := "<ul>\n    <li>old</li>\n</ul>\n"
	got, tier, _, err := applyEdits(content, []Edit{
		{OldString: "    <li>old</li>", NewString: "  <li>new</li>"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier != tierExact {
		t.Fatalf("expected tier %s, got %s", tierExact, tier)
	}
	want := "<ul>\n  <li>new</li>\n</ul>\n"
	if got != want {
		t.Errorf("got %q, want %q — new_string must be spliced verbatim at tier exact, no re-indentation", got, want)
	}
}

// TestApplyEdits_CRLFFileMatchesAtTier2AndPreservesLineEndingsElsewhere
// covers the CRLF edge case: the theme store's own line endings are
// preserved everywhere outside the matched region — this only splices the
// matched bytes, it never rewrites the whole file's line endings to
// whatever the model's old_string/new_string happened to use.
func TestApplyEdits_CRLFFileMatchesAtTier2AndPreservesLineEndingsElsewhere(t *testing.T) {
	// old_string uses 6 spaces against the file's real 4 — more, not fewer,
	// so it can't accidentally be a literal byte substring of the real line
	// (see TestApplyEdits_Tier2MatchesWrongIndentation on why that matters).
	content := "<ul>\r\n    <li>keep</li>\r\n    <li>old</li>\r\n</ul>\r\n"
	got, tier, _, err := applyEdits(content, []Edit{{OldString: "      <li>old</li>", NewString: "      <li>new</li>"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier != tierTrimmed {
		t.Fatalf("expected tier %s, got %s", tierTrimmed, tier)
	}
	want := "<ul>\r\n    <li>keep</li>\r\n    <li>new</li>\r\n</ul>\r\n"
	if got != want {
		t.Errorf("got %q, want %q — CRLF line endings elsewhere in the file must survive untouched", got, want)
	}
}

// fixedFileReader is a FileReader backed by a fixed map — "" (not present)
// means "doesn't exist", matching FileReader's own convention.
func fixedFileReader(files map[string]string) FileReader {
	return func(_ context.Context, path string) (string, error) {
		return files[path], nil
	}
}

func TestMaterializeEdits_SuccessConvertsEditToUpdate(t *testing.T) {
	result := &Result{Files: []GeneratedFile{
		{Path: "components/footer.liquid", Action: "edit", Edits: []Edit{{OldString: "old", NewString: "new"}}},
	}}
	readFile := fixedFileReader(map[string]string{"components/footer.liquid": "before old after"})

	ok, msg := materializeEdits(context.Background(), result, readFile, map[string]int{})
	if !ok {
		t.Fatalf("expected materialization to succeed, got message: %s", msg)
	}
	f := result.Files[0]
	if f.Action != "update" {
		t.Errorf("expected action to become \"update\", got %q", f.Action)
	}
	if f.Content != "before new after" {
		t.Errorf("unexpected materialized content: %q", f.Content)
	}
	if len(f.Edits) != 0 {
		t.Errorf("expected Edits cleared after materialization, got %+v", f.Edits)
	}
}

func TestMaterializeEdits_UnmaterializedFilesUnaffected(t *testing.T) {
	result := &Result{Files: []GeneratedFile{
		{Path: "pages/new.liquid", Action: "create", Content: "hello"},
	}}
	ok, _ := materializeEdits(context.Background(), result, fixedFileReader(nil), map[string]int{})
	if !ok {
		t.Fatal("expected materialization to succeed with no edit-action files")
	}
	if result.Files[0].Action != "create" || result.Files[0].Content != "hello" {
		t.Errorf("expected the create-action file untouched, got %+v", result.Files[0])
	}
}

func TestMaterializeEdits_NonexistentFileFails(t *testing.T) {
	result := &Result{Files: []GeneratedFile{
		{Path: "components/ghost.liquid", Action: "edit", Edits: []Edit{{OldString: "x", NewString: "y"}}},
	}}
	ok, msg := materializeEdits(context.Background(), result, fixedFileReader(nil), map[string]int{})
	if ok {
		t.Fatal("expected materialization to fail for a nonexistent file")
	}
	if msg == "" {
		t.Error("expected a non-empty retry message")
	}
}

func TestMaterializeEdits_DuplicatePathRejected(t *testing.T) {
	result := &Result{Files: []GeneratedFile{
		{Path: "components/footer.liquid", Action: "edit", Edits: []Edit{{OldString: "x", NewString: "y"}}},
		{Path: "components/footer.liquid", Action: "update", Content: "z"},
	}}
	ok, msg := materializeEdits(context.Background(), result, fixedFileReader(map[string]string{"components/footer.liquid": "x"}), map[string]int{})
	if ok {
		t.Fatal("expected edit+update on the same path in one proposal to be rejected")
	}
	if msg == "" {
		t.Error("expected a non-empty retry message naming the duplicate path")
	}
}

func TestMaterializeEdits_EmptyEditsListFails(t *testing.T) {
	result := &Result{Files: []GeneratedFile{
		{Path: "components/footer.liquid", Action: "edit", Edits: nil},
	}}
	ok, _ := materializeEdits(context.Background(), result, fixedFileReader(map[string]string{"components/footer.liquid": "x"}), map[string]int{})
	if ok {
		t.Fatal("expected action \"edit\" with an empty edits[] to fail")
	}
}

func TestMaterializeEdits_ReadErrorFailsWithoutPanicking(t *testing.T) {
	readFile := func(context.Context, string) (string, error) { return "", errors.New("boom") }
	result := &Result{Files: []GeneratedFile{
		{Path: "components/footer.liquid", Action: "edit", Edits: []Edit{{OldString: "x", NewString: "y"}}},
	}}
	ok, msg := materializeEdits(context.Background(), result, readFile, map[string]int{})
	if ok {
		t.Fatal("expected a read error to fail materialization")
	}
	if msg == "" {
		t.Error("expected a non-empty retry message")
	}
}

func TestMaterializeEdits_TwoFailuresFallsBackToUpdateAdvice(t *testing.T) {
	result := func() *Result {
		return &Result{Files: []GeneratedFile{
			{Path: "components/footer.liquid", Action: "edit", Edits: []Edit{{OldString: "nope", NewString: "y"}}},
		}}
	}
	readFile := fixedFileReader(map[string]string{"components/footer.liquid": "content with no match"})
	counts := map[string]int{}

	_, firstMsg := materializeEdits(context.Background(), result(), readFile, counts)
	if got := "resubmit this file with action \"update\""; strings.Contains(firstMsg, got) {
		t.Errorf("expected the FIRST failure to just describe the problem, not already suggest falling back: %q", firstMsg)
	}

	_, secondMsg := materializeEdits(context.Background(), result(), readFile, counts)
	if got := `resubmit this file with action "update"`; !strings.Contains(secondMsg, got) {
		t.Errorf("expected the SECOND failure for the same file to fall back to requesting full content, got: %q", secondMsg)
	}
}

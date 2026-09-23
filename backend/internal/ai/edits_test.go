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
	// The first edit consumes "target"; the second, now searching content that no longer
	// contains it, fails its own zero-match check. No special-casing needed.
	_, _, _, err := applyEdits("target\n", []Edit{
		{OldString: "target", NewString: "first"},
		{OldString: "target", NewString: "second"},
	})
	if err == nil {
		t.Fatal("expected the second overlapping edit to fail its own uniqueness check")
	}
}

// TestApplyEdits_Tier2MatchesWrongIndentation checks the model reproducing text but not
// exact indentation still matches at tier 2, with the replacement at the FILE's real indentation.
func TestApplyEdits_Tier2MatchesWrongIndentation(t *testing.T) {
	// old_string uses MORE leading spaces than the file's real 4, so it can't accidentally
	// be a literal byte substring of the real line (which would let tier 1 match instead).
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

// TestApplyEdits_Tier3MatchesCollapsedSpacing covers internal whitespace reflow that
// tier 2's per-line trim alone doesn't fix.
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

// TestApplyEdits_Tier2AmbiguousFailsRatherThanFallingThrough checks a tier-2 candidate
// appearing twice fails outright, never falling through to tier 3 or silently picking the first.
func TestApplyEdits_Tier2AmbiguousFailsRatherThanFallingThrough(t *testing.T) {
	// Both occurrences trim-match "<li>dup</li>"; tier 3 would ALSO match both, so this
	// can't accidentally pass by falling through either.
	content := "<ul>\n  <li>dup</li>\n    <li>dup</li>\n</ul>\n"
	_, _, count, err := applyEdits(content, []Edit{{OldString: "<li>dup</li>", NewString: "<li>x</li>"}})
	if err == nil {
		t.Fatal("expected an error for a tier-2 candidate matching twice")
	}
	if count != 2 {
		t.Errorf("expected matchCount 2, got %d", count)
	}
}

// TestApplyEdits_AllThreeTiersZeroFails checks a genuinely absent old_string fails
// cleanly, not a panic or false match, when none of the three tiers find anything.
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

// TestApplyEdits_WhitespaceOnlyOldStringDoesNotMatchEveryBlankLine checks a purely-whitespace
// old_string doesn't resolve against an arbitrary blank line under trimmed comparison.
func TestApplyEdits_WhitespaceOnlyOldStringDoesNotMatchEveryBlankLine(t *testing.T) {
	content := "line one\n\nline three\n" // exactly one blank line
	_, _, _, err := applyEdits(content, []Edit{{OldString: "   ", NewString: "x"}})
	if err == nil {
		t.Fatal("expected a whitespace-only old_string to fail rather than match the file's one blank line")
	}
}

// TestApplyEdits_ByteOffsetCorrectnessInLargeMixedIndentFile checks a tier-2 edit late in a
// large, mixed-indent file splices at exactly the right place — nothing else may shift.
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

	// old_string uses MORE leading spaces (6) than the target's real 4, so it can't
	// accidentally be a substring that lets tier 1 match instead.
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

// TestReindentToMatch_PreservesRelativeNesting checks only the DELTA between inserted's own
// first line and subsequent lines is preserved on top of the target — not flattened uniformly.
func TestReindentToMatch_PreservesRelativeNesting(t *testing.T) {
	// File's real indentation is 4 spaces; new_string was written against 6-space
	// indentation with a nested child +2 deeper, closing tag realigned back.
	content := "<div>\n  <ul>\n    <li>old</li>\n  </ul>\n</div>\n"
	newString := "      <li>new\n        <span>nested</span>\n      </li>"
	got, tier, _, err := applyEdits(content, []Edit{{OldString: "      <li>old</li>", NewString: newString}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier != tierTrimmed {
		t.Fatalf("expected tier %s, got %s", tierTrimmed, tier)
	}
	// First line lands at the target's 4-space indentation; the nested line keeps its own
	// +2-space delta (6, not flattened to 4); the closing tag realigns back to 4.
	want := "<div>\n  <ul>\n    <li>new\n      <span>nested</span>\n    </li>\n  </ul>\n</div>\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestReindentToMatch_ClosingCascade checks new_string dedenting across several lines as it
// closes nested elements shifts every line by the same signed delta, not flattened to the first.
func TestReindentToMatch_ClosingCascade(t *testing.T) {
	matched := "    <div>old</div>"                                           // target: 4 spaces
	inserted := "        <p>Powered By FlowPOS</p>\n      </div>\n    </div>" // 8/6/4
	got := reindentToMatch(matched, inserted)
	want := "    <p>Powered By FlowPOS</p>\n  </div>\n</div>" // 4/2/0
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestReindentToMatch_DeeperNestingStillWorks checks a child line indented deeper than
// new_string's first line keeps that extra depth on top of the shift.
func TestReindentToMatch_DeeperNestingStillWorks(t *testing.T) {
	matched := "    <li>old</li>"                                         // target: 4 spaces
	inserted := "      <li>new\n        <span>nested</span>\n      </li>" // 6/8/6
	got := reindentToMatch(matched, inserted)
	want := "    <li>new\n      <span>nested</span>\n    </li>" // 4/6/4
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestReindentToMatch_ClampsNegativeDeltaToZero checks a delta steep enough to push a
// shallow line negative lands at column 0, rather than panicking on a negative Repeat count.
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

// TestReindentToMatch_TabTargetZeroDeltaEmitsSpacesNotFourTabs checks output is always
// plain spaces, never literal tab characters, even at zero delta on a tab-indented target.
func TestReindentToMatch_TabTargetZeroDeltaEmitsSpacesNotFourTabs(t *testing.T) {
	matched := "\t<div>old</div>" // target: 1 tab = width 4
	inserted := "\t<p>new</p>"    // source: 1 tab = width 4, so delta = 0
	got := reindentToMatch(matched, inserted)
	want := "    <p>new</p>" // 4 spaces — NOT "\t\t\t\t"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestReindentToMatch_TabTargetClosingCascade is the tab-indented equivalent of the
// space-indented closing-cascade test; widths still shift by the same signed delta.
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

// TestReindentToMatch_MixedTabAndSpaceInsertedNormalizesToSpaces checks a new_string that
// mixes tabs and spaces re-indents to plain spaces at correct computed widths.
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

// TestApplyEdits_ExactTierNeverReindents checks tier 1 splices new_string byte-for-byte
// with no re-indentation, since an exact match means the model already copied real indentation.
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

// TestApplyEdits_CRLFFileMatchesAtTier2AndPreservesLineEndingsElsewhere checks the file's
// CRLF line endings outside the matched region are preserved, never rewritten wholesale.
func TestApplyEdits_CRLFFileMatchesAtTier2AndPreservesLineEndingsElsewhere(t *testing.T) {
	// old_string uses 6 spaces against the file's real 4, so it can't accidentally be a
	// literal byte substring of the real line.
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

// fixedFileReader is a FileReader backed by a fixed map; "" means "doesn't exist".
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

// TestMaterializeEdits_SetsOriginalActionToEdit checks materializeEdits captures what the
// model submitted before overwriting Action, so recapAssistantTurn can show its real last turn.
func TestMaterializeEdits_SetsOriginalActionToEdit(t *testing.T) {
	result := &Result{Files: []GeneratedFile{
		{Path: "components/footer.liquid", Action: "edit", Edits: []Edit{{OldString: "old", NewString: "new"}}},
	}}
	readFile := fixedFileReader(map[string]string{"components/footer.liquid": "before old after"})

	ok, msg := materializeEdits(context.Background(), result, readFile, map[string]int{})
	if !ok {
		t.Fatalf("expected materialization to succeed, got message: %s", msg)
	}
	if result.Files[0].OriginalAction != "edit" {
		t.Errorf("expected OriginalAction %q, got %q", "edit", result.Files[0].OriginalAction)
	}
}

// TestMaterializeEdits_NeverEditLeavesOriginalActionEmpty checks a file that was never
// "edit" never has OriginalAction set — empty means "same as Action" to readers.
func TestMaterializeEdits_NeverEditLeavesOriginalActionEmpty(t *testing.T) {
	result := &Result{Files: []GeneratedFile{
		{Path: "pages/new.liquid", Action: "create", Content: "hello"},
	}}
	ok, _ := materializeEdits(context.Background(), result, fixedFileReader(nil), map[string]int{})
	if !ok {
		t.Fatal("expected materialization to succeed with no edit-action files")
	}
	if result.Files[0].OriginalAction != "" {
		t.Errorf("expected OriginalAction to stay empty for a file that was never an edit, got %q", result.Files[0].OriginalAction)
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

// TestMaterializeEdits_DuplicatePathsTwiceFailsGeneration checks the duplicate-paths
// failure is strike-limited, bounding how many propose_changes round trips it can burn.
func TestMaterializeEdits_DuplicatePathsTwiceFailsGeneration(t *testing.T) {
	result := func() *Result {
		return &Result{Files: []GeneratedFile{
			{Path: "components/footer.liquid", Action: "edit", Edits: []Edit{{OldString: "x", NewString: "y"}}},
			{Path: "components/footer.liquid", Action: "update", Content: "z"},
		}}
	}
	readFile := fixedFileReader(map[string]string{"components/footer.liquid": "x"})
	counts := map[string]int{}

	_, firstMsg := materializeEdits(context.Background(), result(), readFile, counts)
	if strings.Contains(firstMsg, "fail the generation") {
		t.Errorf("expected the FIRST duplicate-paths failure to just describe the problem, got: %q", firstMsg)
	}

	_, secondMsg := materializeEdits(context.Background(), result(), readFile, counts)
	if !strings.Contains(secondMsg, "fail the generation") {
		t.Errorf("expected the SECOND duplicate-paths failure in a row to warn the generation will fail, got: %q", secondMsg)
	}
	// The original merge guidance must still be there — the correct fix never changes.
	if !strings.Contains(secondMsg, "combine every change to one file into a single files[] entry") {
		t.Errorf("expected the original merge guidance to still be present at the limit, got: %q", secondMsg)
	}
}

// TestMaterializeEdits_NonexistentFileTwiceFailsGeneration checks the file-not-found
// failure is strike-limited, bounding how many times a model can propose "edit" for a missing path.
func TestMaterializeEdits_NonexistentFileTwiceFailsGeneration(t *testing.T) {
	result := func() *Result {
		return &Result{Files: []GeneratedFile{
			{Path: "components/ghost.liquid", Action: "edit", Edits: []Edit{{OldString: "x", NewString: "y"}}},
		}}
	}
	readFile := fixedFileReader(nil) // "" for every path — nothing exists
	counts := map[string]int{}

	_, firstMsg := materializeEdits(context.Background(), result(), readFile, counts)
	if strings.Contains(firstMsg, "fail the generation") {
		t.Errorf("expected the FIRST file-not-found failure to just describe the problem, got: %q", firstMsg)
	}
	if !strings.Contains(firstMsg, `action "create"`) {
		t.Errorf("expected the original create-instead-of-edit guidance, got: %q", firstMsg)
	}

	_, secondMsg := materializeEdits(context.Background(), result(), readFile, counts)
	if !strings.Contains(secondMsg, "fail the generation") {
		t.Errorf("expected the SECOND file-not-found failure in a row to warn the generation will fail, got: %q", secondMsg)
	}
	if !strings.Contains(secondMsg, `action "create"`) {
		t.Errorf("expected the original create-instead-of-edit guidance to still be present at the limit, got: %q", secondMsg)
	}
}

// TestMaterializeEdits_NoMatchIncludesContentWhenUnderCap checks a no_match failure under
// noMatchContentCap includes the file's real content directly, instead of forcing a re-read.
func TestMaterializeEdits_NoMatchIncludesContentWhenUnderCap(t *testing.T) {
	content := "line one\nline two\nline three\n"
	result := &Result{Files: []GeneratedFile{
		{Path: "components/footer.liquid", Action: "edit", Edits: []Edit{{OldString: "nope", NewString: "y"}}},
	}}
	readFile := fixedFileReader(map[string]string{"components/footer.liquid": content})

	ok, msg := materializeEdits(context.Background(), result, readFile, map[string]int{})
	if ok {
		t.Fatal("expected materialization to fail")
	}
	if !strings.Contains(msg, content) {
		t.Errorf("expected the real file content inlined in the retry message, got: %q", msg)
	}
}

// TestMaterializeEdits_NoMatchOverCapShowsNearMissWindow checks a file too large to inline
// whole still gets a bounded window around a cheap anchor, not the entire file.
func TestMaterializeEdits_NoMatchOverCapShowsNearMissWindow(t *testing.T) {
	anchor := "TARGET LINE"
	big := strings.Repeat("filler ", 2000) + anchor + strings.Repeat(" more filler", 2000)
	result := &Result{Files: []GeneratedFile{
		{Path: "components/footer.liquid", Action: "edit",
			Edits: []Edit{{OldString: anchor + "\nnope", NewString: "y"}}},
	}}
	readFile := fixedFileReader(map[string]string{"components/footer.liquid": big})

	ok, msg := materializeEdits(context.Background(), result, readFile, map[string]int{})
	if ok {
		t.Fatal("expected materialization to fail")
	}
	if !strings.Contains(msg, anchor) {
		t.Errorf("expected the near-miss window to include the anchor line, got: %q", msg)
	}
	if strings.Contains(msg, big) {
		t.Error("expected only a bounded window, not the entire oversized file, inlined")
	}
}

// TestMaterializeEdits_NoMatchOverCapNoAnchorSaysReRead checks a file too large to inline
// with no cheap anchor falls back to a plain re-read instruction, never silently drops the failure.
func TestMaterializeEdits_NoMatchOverCapNoAnchorSaysReRead(t *testing.T) {
	big := strings.Repeat("x", noMatchContentCap+1000)
	result := &Result{Files: []GeneratedFile{
		{Path: "components/footer.liquid", Action: "edit",
			Edits: []Edit{{OldString: "this text is nowhere in the file", NewString: "y"}}},
	}}
	readFile := fixedFileReader(map[string]string{"components/footer.liquid": big})

	ok, msg := materializeEdits(context.Background(), result, readFile, map[string]int{})
	if ok {
		t.Fatal("expected materialization to fail")
	}
	if !strings.Contains(msg, "re-read this one file specifically") {
		t.Errorf("expected the explicit re-read instruction when no near-miss anchor was found, got: %q", msg)
	}
}

// overlayReader mirrors themebuild's repairFileReader (a map checked first, falling back to
// base) without depending on that package.
func overlayReader(overlay map[string]string, base FileReader) FileReader {
	return func(ctx context.Context, path string) (string, error) {
		if content, ok := overlay[path]; ok {
			return content, nil
		}
		return base(ctx, path)
	}
}

// TestMaterializeEdits_SucceedsAgainstOverlayOnlyFile checks a file that only exists in a
// rejected proposal's own overlay (nothing staged to the real store yet) still materializes.
func TestMaterializeEdits_SucceedsAgainstOverlayOnlyFile(t *testing.T) {
	base := fixedFileReader(nil) // nothing in the store
	reader := overlayReader(map[string]string{"components/home-bestsellers.liquid": "<div>old</div>"}, base)

	result := &Result{Files: []GeneratedFile{
		{Path: "components/home-bestsellers.liquid", Action: "edit",
			Edits: []Edit{{OldString: "old", NewString: "new"}}},
	}}
	ok, msg := materializeEdits(context.Background(), result, reader, map[string]int{})
	if !ok {
		t.Fatalf("expected materialization to succeed via the overlay, got: %s", msg)
	}
	if result.Files[0].Content != "<div>new</div>" {
		t.Errorf("unexpected materialized content: %q", result.Files[0].Content)
	}
}

// TestMaterializeEdits_NoReExploreInstructionAppearsOnceForBatch checks three failures at
// once carry the no-re-explore instruction exactly once, not once per failure.
func TestMaterializeEdits_NoReExploreInstructionAppearsOnceForBatch(t *testing.T) {
	result := &Result{Files: []GeneratedFile{
		{Path: "a.liquid", Action: "edit", Edits: []Edit{{OldString: "nope-a", NewString: "y"}}},
		{Path: "b.liquid", Action: "edit", Edits: []Edit{{OldString: "nope-b", NewString: "y"}}},
		{Path: "c.liquid", Action: "edit", Edits: nil},
	}}
	readFile := fixedFileReader(map[string]string{
		"a.liquid": "content a",
		"b.liquid": "content b",
	})

	ok, msg := materializeEdits(context.Background(), result, readFile, map[string]int{})
	if ok {
		t.Fatal("expected materialization to fail")
	}
	want := "Do not explore, read, or touch anything else"
	if got := strings.Count(msg, want); got != 1 {
		t.Errorf("expected the no-re-explore instruction exactly once for a 3-failure batch, got %d occurrences: %q", got, msg)
	}
}

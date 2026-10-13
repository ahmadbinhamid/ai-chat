package ai

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// largeFooterCSS builds a stylesheet over noMatchContentCap whose rules repeat one declaration, like the theme's
// footer.css (14 KB, "color: #ffffff !important;" 13 times).
func largeFooterCSS(rules int) string {
	var b strings.Builder
	for i := 0; i < rules; i++ {
		fmt.Fprintf(&b, ".footer-col-%d a {\n  color: #ffffff !important;\n  text-decoration: none;\n}\n\n", i)
	}
	for b.Len() <= noMatchContentCap {
		b.WriteString("/* padding so the file is too large to inline in full */\n")
	}
	return b.String()
}

func TestNoMatchProblem_AmbiguousMatchListsEveryOccurrence(t *testing.T) {
	content := largeFooterCSS(3)
	edits := []Edit{{OldString: "  color: #ffffff !important;", NewString: "  color: var(--footer-link, #cbd5e1) !important;"}}

	_, _, count, err := applyEdits(content, edits)
	var ambiguous *ambiguousMatchError
	if !errors.As(err, &ambiguous) || count != 3 {
		t.Fatalf("want an ambiguous match of 3, got count=%d err=%v", count, err)
	}
	if !strings.Contains(err.Error(), "matched 3 times") {
		t.Errorf("the error wording must not change, got %q", err)
	}

	msg := noMatchProblem("components/css/footer.css", content, edits, err)
	for _, want := range []string{"Occurrence 1 (line 2)", "Occurrence 2 (line 7)", "Occurrence 3 (line 12)",
		".footer-col-1 a {", ">    7 |   color: #ffffff !important;"} {
		if !strings.Contains(msg, want) {
			t.Errorf("retry message missing %q:\n%s", want, msg)
		}
	}

	// The retry the listing makes possible: the second occurrence's selector line copied from it matches only there.
	retry := []Edit{{OldString: ".footer-col-1 a {\n  color: #ffffff !important;", NewString: ".footer-col-1 a {\n  color: var(--footer-link, #cbd5e1) !important;"}}
	got, _, _, err := applyEdits(content, retry)
	if err != nil || strings.Count(got, "var(--footer-link") != 1 || !strings.Contains(got, ".footer-col-1 a {\n  color: var(--footer-link") {
		t.Fatalf("want the disambiguated retry to change only occurrence 2, err=%v", err)
	}
}

func TestNoMatchProblem_AmbiguousMatchOnTheTrimmedTier(t *testing.T) {
	content := largeFooterCSS(2)
	// Different indentation, so only the trimmed tier matches, and it matches twice.
	edits := []Edit{{OldString: "    color: #ffffff !important;\n    text-decoration: none;", NewString: "x"}}
	_, _, count, err := applyEdits(content, edits)
	var ambiguous *ambiguousMatchError
	if !errors.As(err, &ambiguous) || count != 2 || !strings.Contains(err.Error(), "matched 2 locations") {
		t.Fatalf("want a 2-location ambiguous match on the trimmed tier, got count=%d err=%v", count, err)
	}
	msg := noMatchProblem("components/css/footer.css", content, edits, err)
	if !strings.Contains(msg, "Occurrence 2 (line 7)") || !strings.Contains(msg, ">    8 |   text-decoration: none;") {
		t.Errorf("want both lines of each occurrence marked:\n%s", msg)
	}
}

func TestNoMatchProblem_AmbiguousListingIsCapped(t *testing.T) {
	content := largeFooterCSS(10)
	edits := []Edit{{OldString: "  color: #ffffff !important;", NewString: "x"}}
	_, _, _, err := applyEdits(content, edits)
	msg := noMatchProblem("components/css/footer.css", content, edits, err)
	if strings.Count(msg, "Occurrence ") != maxAmbiguousMatchesShown || !strings.Contains(msg, "…and 4 more.") {
		t.Errorf("want %d occurrences listed and the rest counted:\n%s", maxAmbiguousMatchesShown, msg)
	}
}

// A small file is still inlined whole, as before; the listing only fills the gap for files too large to inline.
func TestNoMatchProblem_SmallFileStillInlinedWhole(t *testing.T) {
	content := ".a {\n  color: #fff;\n}\n.b {\n  color: #fff;\n}\n"
	edits := []Edit{{OldString: "  color: #fff;", NewString: "x"}}
	_, _, _, err := applyEdits(content, edits)
	msg := noMatchProblem("x.css", content, edits, err)
	if !strings.Contains(msg, "real current content") || strings.Contains(msg, "Occurrence 1") {
		t.Errorf("want the whole small file inlined as before:\n%s", msg)
	}
}

func TestExactMatchStarts(t *testing.T) {
	for _, tt := range []struct {
		content, sub string
		want         []int
	}{
		{"aXbXc", "X", []int{1, 3}},
		{"aaaa", "aa", []int{0, 2}},
		{"abc", "z", nil},
	} {
		if got := exactMatchStarts(tt.content, tt.sub); fmt.Sprint(got) != fmt.Sprint(tt.want) {
			t.Errorf("exactMatchStarts(%q, %q) = %v, want %v", tt.content, tt.sub, got, tt.want)
		}
	}
}

package themebuild

import "testing"

func TestClassifyIntent_FooterCSSNotApplied(t *testing.T) {
	cases := []string{
		"your footer desgin css e ni apply hoi kye bkws hn",
		"footer css not applied",
		"css nahi apply hui footer pe",
		"the footer design css didn't apply",
		"footer style ni apply ho rahi",
	}
	for _, p := range cases {
		got := ClassifyIntent(p, "", false)
		if got != IntentComplexPage {
			t.Fatalf("%q: want complex_page got %s (section=%v cssBroken=%v)",
				p, got, isSectionRedesignPrompt(p), isSectionCSSBrokenPrompt(p))
		}
	}
	// Tiny color tweak must stay simple_edit.
	if got := ClassifyIntent("make the footer text white", "", false); got != IntentSimpleEdit {
		t.Fatalf("tiny tweak want simple_edit got %s", got)
	}
}

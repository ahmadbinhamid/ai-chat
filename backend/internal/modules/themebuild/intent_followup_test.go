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
	if got := ClassifyIntent("make the footer copyright text a bit lighter gray", "", false); got != IntentSimpleEdit {
		t.Fatalf("copyright color tweak want simple_edit got %s", got)
	}
}

func TestClassifyIntent_HomeLooksLikeHTML(t *testing.T) {
	cases := []string{
		"home page is not correct and home desgin is only like html not any css apply ?",
		"homepage looks like plain html no css",
		"home page css not applied",
		"homepage design only like html",
	}
	for _, p := range cases {
		got := ClassifyIntent(p, "", false)
		if got != IntentComplexPage {
			t.Fatalf("%q: want complex_page got %s (fullHome=%v homeCSS=%v)",
				p, got, isFullHomePageRedesignPrompt(p), isHomeCSSBrokenPrompt(p))
		}
		if !isFullHomePageRedesignPrompt(p) {
			t.Fatalf("%q: expected full-home packaging", p)
		}
	}
}

func TestClassifyIntent_StillLooksTheSame(t *testing.T) {
	cases := []string{
		"abi b wasy e dek rha",
		"still looking the same",
		"still looks the same",
		"nothing changed",
	}
	for _, p := range cases {
		got := ClassifyIntent(p, "", false)
		if got != IntentRepair {
			t.Fatalf("%q: want repair got %s", p, got)
		}
	}
}

func TestClassifyIntent_HomeReferenceClone(t *testing.T) {
	cases := []string{
		"http://sales.ms/ mjy home page asa chy proper diko or same kro pelase",
		"same site asi ready hn home page ?",
		"mjy home page asa chy proper diko or same kro please",
		"make my homepage look like this https://example.com",
		"homepage same as this site please",
	}
	for _, p := range cases {
		got := ClassifyIntent(p, "", true)
		if got != IntentComplexPage {
			t.Fatalf("%q: want complex_page got %s (clone=%v fullHome=%v)",
				p, got, isHomeReferenceClonePrompt(p), isFullHomePageRedesignPrompt(p))
		}
		if !isFullHomePageRedesignPrompt(p) {
			t.Fatalf("%q: expected full-home packaging for reference clone", p)
		}
	}
}

func TestClassifyIntent_PrivacyBrandScrubNotSimpleEdit(t *testing.T) {
	cases := []string{
		"update the privacy page according to software company please contnent abi b jpro ka arha mjy kisi b page py jpro ni chy please sari fies dek lo theme k",
		"update the privacy page according to software company",
		"jpro ni chy kisi b page py",
		"remove jpro from all pages",
		"sari files dek lo theme ki no jpro",
		"rewrite privacy policy for saas company",
	}
	for _, p := range cases {
		got := ClassifyIntent(p, "", false)
		if got != IntentComplexPage {
			t.Fatalf("%q: want complex_page got %s (brand=%v)",
				p, got, isBrandScrubOrLegalRewritePrompt(p))
		}
		if intentUsesSimpleEditOneShot(got) {
			t.Fatalf("%q: must not use simple_edit one-shot", p)
		}
	}
	// Tiny privacy CSS color tweak can stay simple_edit.
	if got := ClassifyIntent("make the privacy page heading blue", "", false); got != IntentSimpleEdit {
		t.Fatalf("tiny tweak want simple_edit got %s", got)
	}
}

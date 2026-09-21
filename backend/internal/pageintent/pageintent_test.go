package pageintent

import "testing"

func TestDetectRegisterExisting_Matches(t *testing.T) {
	cases := []struct {
		prompt   string
		wantName string
	}{
		{"register the pricing page", "pricing"},
		{"Register the Pricing page", "Pricing"},
		{"please register the about-us page", "about-us"},
		{"can you register my contact page", "contact"},
		{"could you register this careers page", "careers"},
		{"register our terms and conditions page", "terms and conditions"},
	}
	for _, c := range cases {
		name, ok := DetectRegisterExisting(c.prompt)
		if !ok {
			t.Errorf("DetectRegisterExisting(%q): expected a match, got none", c.prompt)
			continue
		}
		if name != c.wantName {
			t.Errorf("DetectRegisterExisting(%q) name = %q, want %q", c.prompt, name, c.wantName)
		}
	}
}

// TestDetectRegisterExisting_RejectsNonMatches covers every reason a
// prompt must NOT trigger the deterministic register path — see the
// package doc comment on why a false positive here is the real risk.
func TestDetectRegisterExisting_RejectsNonMatches(t *testing.T) {
	for _, prompt := range []string{
		// No page name to resolve — "the page" alone isn't enough context.
		"register the page",
		// "register" not opening the request — the theme's own signup page
		// (a real systemPageTypes entry), not a registration request.
		"fix the register page",
		"the register page is broken",
		"why does the register page look bad",
		// Create, not register — a fundamentally different operation.
		"create a new pricing page",
		"add a pricing page",
		// Register + an edit cue in the same prompt — the merchant wants
		// more than just registration, defer to normal generation.
		"register the pricing page and redesign it",
		"register the about page, change the color while you're at it",
		"register the contact page and add a section for our hours",
		// No "register" verb at all.
		"the pricing page needs work",
		"diagnose the pricing page",
		// Empty / whitespace only.
		"",
		"   ",
		// Too long — a multi-part request, not a narrow single-purpose ask.
		"register the pricing page and also please rewrite the homepage hero section with a new headline about our summer sale and update the footer links to point to our new social media accounts",
	} {
		if name, ok := DetectRegisterExisting(prompt); ok {
			t.Errorf("DetectRegisterExisting(%q): expected no match, got name=%q", prompt, name)
		}
	}
}

func TestDetectDiagnoseExisting_Matches(t *testing.T) {
	cases := []struct {
		prompt   string
		wantName string
	}{
		{"the pricing page is not working", "pricing"},
		{"contact page won't open", "contact"},
		{"why is the checkout page not loading for customers", "checkout"},
		{"the about us page is broken", "about us"},
		{"my careers page gives a 404", "careers"},
		{"the blog page stopped working", "blog"},
		// "register" as the page's actual NAME (the theme's own signup
		// page — see themecheck's systemPageTypes), not the command verb —
		// resolved correctly here specifically because diagnose's stopword
		// set (unlike register's own) never strips "register" itself.
		{"the register page is not working", "register"},
	}
	for _, c := range cases {
		name, ok := DetectDiagnoseExisting(c.prompt)
		if !ok {
			t.Errorf("DetectDiagnoseExisting(%q): expected a match, got none", c.prompt)
			continue
		}
		if name != c.wantName {
			t.Errorf("DetectDiagnoseExisting(%q) name = %q, want %q", c.prompt, name, c.wantName)
		}
	}
}

func TestDetectDiagnoseExisting_RejectsNonMatches(t *testing.T) {
	for _, prompt := range []string{
		// No trouble phrase at all — a plain content/design request.
		"make the pricing page nicer",
		"redesign the pricing page",
		"the pricing page needs a facelift",
		// Trouble phrase but no resolvable page name.
		"something is not working",
		"the page is broken",
		// Trouble phrase + an edit cue — treat as a real change request.
		"the pricing page is broken, please redesign it",
		"fix the pricing page, it's not working",
		"",
		"   ",
	} {
		if name, ok := DetectDiagnoseExisting(prompt); ok {
			t.Errorf("DetectDiagnoseExisting(%q): expected no match, got name=%q", prompt, name)
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"pricing", "pricing"},
		{"Pricing", "pricing"},
		{"Contact Us", "contact-us"},
		{"  Pricing  ", "pricing"},
		{"about-us", "about-us"},
		{"Terms & Conditions", "terms-conditions"},
		{"", ""},
		{"---", ""},
	}
	for _, c := range cases {
		if got := Slugify(c.name); got != c.want {
			t.Errorf("Slugify(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

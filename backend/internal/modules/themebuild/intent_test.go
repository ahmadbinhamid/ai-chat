package themebuild

import "testing"

func TestClassifyIntent_Conversation(t *testing.T) {
	cases := []string{
		"hi", "hello", "hey", "hi there", "good morning",
		"thanks", "thank you", "ok", "okay", "asdf", "hi?", "hmm",
	}
	for _, p := range cases {
		if got := ClassifyIntent(p, "", false); got != IntentConversation {
			t.Errorf("ClassifyIntent(%q)=%s want conversation", p, got)
		}
	}
}

func TestClassifyIntent_ThemeEdit(t *testing.T) {
	cases := []struct {
		prompt string
		want   Intent
	}{
		{"change the header", IntentSimpleEdit},
		{"can you change the header desgin please do it fast", IntentSimpleEdit},
		{"make the header modern", IntentSimpleEdit},
		{"change button color", IntentSimpleEdit},
		{"change the header color", IntentSimpleEdit},
		{"update logo in header", IntentSimpleEdit},
		{"update homepage hero", IntentSimpleEdit},
		{"change the homepage hero color", IntentSimpleEdit},
		{"add a section", IntentSimpleEdit},
		{"change product card design", IntentSimpleEdit},
		{"fix the broken header", IntentRepair},
		{"rebuild the entire theme from scratch", IntentComplexPage},
		{"change the header and footer", IntentMultiFileEdit},
		{"redesign the footer", IntentComplexPage},
		{"redesign the footer into a modern premium SaaS footer with newsletter", IntentComplexPage},
		{"make the footer text white", IntentSimpleEdit},
		{"can you change the home page desgin ? proper with a slider and and beautifull home page", IntentComplexPage},
		{"redesign the homepage with a slider", IntentComplexPage},
		{"make a beautiful homepage", IntentComplexPage},
		{"change home page design", IntentComplexPage},
		{"an we use multilpal imges on slider and auto scrol please", IntentComplexPage},
		{"can we use multiple images on slider and auto scroll please", IntentComplexPage},
		{"enable autoplay on the carousel", IntentComplexPage},
		{"change the slider color", IntentSimpleEdit},
	}
	for _, tc := range cases {
		got := ClassifyIntent(tc.prompt, "", false)
		if got != tc.want {
			t.Errorf("ClassifyIntent(%q)=%s want %s", tc.prompt, got, tc.want)
		}
		if tc.want == IntentSimpleEdit && !intentUsesSimpleEditOneShot(got) {
			t.Errorf("ClassifyIntent(%q): simple_edit must use one-shot gate", tc.prompt)
		}
	}
}

func TestClassifyIntent_PageCreateNotSimpleEdit(t *testing.T) {
	cases := []string{
		"create a Contact Us page",
		"create a page and add it to menu",
		"add a new FAQ page",
		"create a landing page",
		"make a page for about us",
		"create About page",
		"add a new page",
		"new page for contact",
		"Create a new Contact Us page and add it to the menu",
		"ek page create kr dty ho test jis me bs contact us ka page ho or wo menu me b add kr do",
		"add contact us to the menu",
		"create FAQ page and add to navigation",
		"please genrate 10 blogs pages for software house company page related",
		"generate 5 blog pages",
	}
	for _, p := range cases {
		got := ClassifyIntent(p, "", false)
		if got == IntentSimpleEdit {
			t.Errorf("ClassifyIntent(%q)=simple_edit — must be complex_page (page create / menu)", p)
		}
		if got != IntentComplexPage {
			t.Errorf("ClassifyIntent(%q)=%s want complex_page", p, got)
		}
		if intentUsesSimpleEditOneShot(got) {
			t.Errorf("ClassifyIntent(%q): must not enter simple-edit one-shot", p)
		}
	}
}

func TestClassifyIntent_PageContentRewriteNotSimpleEdit(t *testing.T) {
	cases := []string{
		"bhi software company realted conent add kro services page me please zada sara please",
		"rewrite faq page according to software company",
		"recreate faq page according to software company",
		"add lots of content to the about page",
		"shop k page ko software compnay theme k mutibq regenrate kro please",
		"shop page ko software company theme ke mutabiq regenerate kro",
		"yaha abi b banner jpro ka arha or bki chizy b theme b jpro ka tm kych b change ni kye shop page",
		"page ki css ni apply ho rhi please css proper kro shop page ki please",
		"and also change the blogs according to software house please and still not change the jpro meta titles form this site",
		"change the blogs according to software house",
		"still not change the jpro meta titles from this site",
	}
	for _, p := range cases {
		got := ClassifyIntent(p, "", false)
		if got == IntentSimpleEdit || got == IntentMultiFileEdit {
			t.Errorf("ClassifyIntent(%q)=%s — want complex_page (content/blog/meta rewrite)", p, got)
		}
		if got != IntentComplexPage {
			t.Errorf("ClassifyIntent(%q)=%s want complex_page", p, got)
		}
		if intentUsesSimpleEditOneShot(got) {
			t.Errorf("%q must not use simple_edit one-shot", p)
		}
	}
	if !isNamedPageCSSBrokenPrompt("page ki css ni apply ho rhi please css proper kro shop page ki please") {
		t.Fatal("shop css ni apply must be named-page CSS broken")
	}
	if promptNamedPageSlug("shop k page ko software compnay theme k mutibq regenrate kro please") != "products" {
		t.Fatal("shop page must map to products slug")
	}
}

func TestRequestedNewPageCount(t *testing.T) {
	if got := requestedNewPageCount("please genrate 10 blogs pages for software house"); got != 10 {
		t.Fatalf("want 10 got %d", got)
	}
	if !isMultiPageCreatePrompt("please genrate 10 blogs pages for software house company page related") {
		t.Fatal("expected multi-page create")
	}
	if isMultiPageCreatePrompt("where you add pages.json ?") {
		t.Fatal("pages.json question must not be multi-page create")
	}
}

func TestClassifyIntent_ThemeQueryNotConversation(t *testing.T) {
	cases := []string{
		"what is the header file?",
		"show me the header",
		"find the product card",
		"what files control the homepage?",
	}
	for _, p := range cases {
		got := ClassifyIntent(p, "", false)
		if got == IntentConversation {
			t.Errorf("ClassifyIntent(%q)=conversation — must use theme path", p)
		}
	}
}

func TestClassifyIntent_AmbiguousFallsToTheme(t *testing.T) {
	got := ClassifyIntent("can you help with something on my store", "", false)
	if got == IntentConversation {
		t.Fatalf("ambiguous prompt must not be conversation, got %s", got)
	}
}

func TestClassifyIntent_AttachmentsForceTheme(t *testing.T) {
	if got := ClassifyIntent("hi", "", true); got == IntentConversation {
		t.Fatal("attachments must not take conversation path")
	}
}

func TestConversationReply(t *testing.T) {
	if r := conversationReply("hi"); r == "" {
		t.Fatal("empty reply")
	}
	if r := conversationReply("thanks"); !containsFold(r, "welcome") {
		t.Fatalf("thanks reply unexpected: %q", r)
	}
}

func containsFold(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub ||
		len(sub) == 0 ||
		(func() bool {
			s, sub = stringsToLower(s), stringsToLower(sub)
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		})())
}

func stringsToLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

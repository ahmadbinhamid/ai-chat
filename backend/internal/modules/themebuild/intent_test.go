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
		{"update homepage hero", IntentSimpleEdit},
		{"add a section", IntentSimpleEdit},
		{"change product card design", IntentSimpleEdit},
		{"fix the broken header", IntentRepair},
		{"rebuild the entire theme from scratch", IntentComplexPage},
		{"change the header and footer", IntentMultiFileEdit},
	}
	for _, tc := range cases {
		if got := ClassifyIntent(tc.prompt, "", false); got != tc.want {
			t.Errorf("ClassifyIntent(%q)=%s want %s", tc.prompt, got, tc.want)
		}
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

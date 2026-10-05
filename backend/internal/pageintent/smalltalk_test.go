package pageintent

import (
	"strings"
	"testing"
)

func TestDetectSmallTalk(t *testing.T) {
	greetings := []string{
		// Every listed phrase.
		"hi", "hello", "hey", "hiya", "howdy", "yo", "hey there", "hi there", "hello there",
		"good morning", "good afternoon", "good evening", "good day", "alright",
		"what's up", "whats up", "sup", "wassup", "how are you", "how are you doing", "how r u",
		"how's it going", "hows it going", "how's things",
		// Combinations, in any order, with punctuation.
		"hey how are you", "hi there, how's it going", "hello good morning", "hey what's up",
		"good morning, how are you doing?", "alright, how's things", "yo sup", "hiya how r u",
		// A mix of both categories is a greeting.
		"hi, thanks", "thanks hi",
		// Stretched spellings, casing, punctuation and emoji.
		"hiii", "heyyy", "hellooo", "heyyyyy there", "Hello!", "  HI  ", "how are you?", "good   morning!!",
		"hey 👋", "👋 hi", "What’s up?",
	}
	acknowledgements := []string{
		"thanks", "thank you", "thanks a lot", "thank you so much", "thx", "ty", "cheers", "ta",
		"ok", "okay", "k", "cool", "got it", "nice", "great", "perfect", "awesome", "brilliant", "lovely",
		"thanks 🙏", "Thank you!", "ok thanks", "cool, got it", "thanksss", "perfect, cheers", "okayyy",
	}
	negatives := []string{
		"hey can you change the header",
		"hi make it blue",
		"thanks, now fix the footer",
		"what's up with my cart page",
		"hi, can you make the header blue",
		"hi can you help",
		"thanks, now make it red",
		"okay make it blue",
		"great job on the header",
		"nice header",
		"hello world page",
		"what can you do?",
		"hi, fix",
		"good",
		"how",
		"up",
		"",
		"🙏",
		strings.Repeat("hello ", 11),
	}

	for _, p := range greetings {
		if got := DetectSmallTalk(p); got != SmallTalkGreeting {
			t.Errorf("DetectSmallTalk(%q) = %d, want greeting", p, got)
		}
	}
	for _, p := range acknowledgements {
		if got := DetectSmallTalk(p); got != SmallTalkAcknowledgement {
			t.Errorf("DetectSmallTalk(%q) = %d, want acknowledgement", p, got)
		}
	}
	for _, p := range negatives {
		if got := DetectSmallTalk(p); got != SmallTalkNone {
			t.Errorf("DetectSmallTalk(%q) = %d, want none", p, got)
		}
	}
}

func TestNormalizeSmallTalk(t *testing.T) {
	tests := map[string]string{
		"Hiii!!":                     "hi",
		"hellooo":                    "hello",
		"cool":                       "cool",
		"what's up?":                 "whats up",
		"hi there, how’s it going 👋": "hi there hows it going",
	}
	for in, want := range tests {
		if got := normalizeSmallTalk(in); got != want {
			t.Errorf("normalizeSmallTalk(%q) = %q, want %q", in, got, want)
		}
	}
}

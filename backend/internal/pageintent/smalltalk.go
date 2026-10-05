package pageintent

import (
	"strings"
	"unicode"
)

// SmallTalk classifies a prompt that is only a greeting or an acknowledgement.
type SmallTalk int

const (
	SmallTalkNone SmallTalk = iota
	SmallTalkGreeting
	SmallTalkAcknowledgement
)

// maxSmallTalkLen is far below maxPromptLen: even a combined greeting ("hi there, how's it going") is a few words.
const maxSmallTalkLen = 60

// Phrases are stored normalized (no apostrophes). A false positive gives a real request a canned reply, so every word
// of a prompt must belong to one of these; a single unlisted word means it isn't small talk.
var greetingPhrases = phraseSet(
	"hi", "hello", "hey", "hiya", "howdy", "yo", "hey there", "hi there", "hello there",
	"good morning", "good afternoon", "good evening", "good day", "alright",
	"whats up", "sup", "wassup", "how are you", "how are you doing", "how r u",
	"hows it going", "hows things",
)

var acknowledgementPhrases = phraseSet(
	"thanks", "thank you", "thanks a lot", "thank you so much", "thx", "ty", "cheers", "ta",
	"ok", "okay", "k", "cool", "got it", "nice", "great", "perfect", "awesome", "brilliant", "lovely",
)

func phraseSet(phrases ...string) map[string]bool {
	set := make(map[string]bool, len(phrases))
	for _, p := range phrases {
		set[p] = true
	}
	return set
}

// maxPhraseWords is the longest listed phrase ("thank you so much", "how are you doing").
const maxPhraseWords = 4

// DetectSmallTalk reports whether the WHOLE prompt is made of greeting/acknowledgement phrases, in any order. A mix of
// both counts as a greeting. "hey how are you" and "hiii 👋" match; "hi make it blue" and "what's up with my cart page" don't.
func DetectSmallTalk(prompt string) SmallTalk {
	if len(prompt) > maxSmallTalkLen || hasEditCue(prompt) {
		return SmallTalkNone
	}
	words := strings.Fields(normalizeSmallTalk(prompt))
	if len(words) == 0 {
		return SmallTalkNone
	}

	// covered[i] is how words[:i] can be covered: 0 not at all, 1 by acknowledgements only, 2 with a greeting among them.
	covered := make([]int, len(words)+1)
	covered[0] = 1
	for i := 1; i <= len(words); i++ {
		for n := 1; n <= maxPhraseWords && n <= i; n++ {
			if covered[i-n] == 0 {
				continue
			}
			phrase := strings.Join(words[i-n:i], " ")
			switch {
			case greetingPhrases[phrase]:
				covered[i] = 2
			case acknowledgementPhrases[phrase]:
				covered[i] = max(covered[i], covered[i-n])
			}
		}
	}
	switch {
	case covered[len(words)] == 2:
		return SmallTalkGreeting
	case covered[len(words)] == 1:
		return SmallTalkAcknowledgement
	default:
		return SmallTalkNone
	}
}

// normalizeSmallTalk lowercases, drops apostrophes ("what's" -> "whats"), turns any other punctuation or emoji into a
// space, and collapses a letter repeated 3+ times to one ("heyyy" -> "hey"); doubles are kept ("hello", "cool").
func normalizeSmallTalk(prompt string) string {
	var rs []rune
	for _, r := range strings.ToLower(prompt) {
		switch {
		case r == '\'' || r == '’':
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			rs = append(rs, r)
		default:
			rs = append(rs, ' ')
		}
	}
	out := make([]rune, 0, len(rs))
	for i := 0; i < len(rs); {
		j := i
		for j < len(rs) && rs[j] == rs[i] {
			j++
		}
		if j-i >= 3 && unicode.IsLetter(rs[i]) {
			out = append(out, rs[i])
		} else {
			out = append(out, rs[i:j]...)
		}
		i = j
	}
	return strings.Join(strings.Fields(string(out)), " ")
}

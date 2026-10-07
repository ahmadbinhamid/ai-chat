package previewerrors

import "regexp"

// UntestableFeatureNote goes on a request about checkout or customer accounts, which have no backend in the preview,
// so the model checks the code instead of hunting for a bug the preview itself causes.
const UntestableFeatureNote = "(Platform note: checkout, login, registration and customer account pages can't run in " +
	"the preview — it has no store behind it. Check the relevant code is correct. If it is, call propose_changes with " +
	"no files and explain that this can't be tested in the preview, instead of changing it. Only change code if you " +
	"find a real bug that would also break the live store.)"

// CartFeatureNote goes on a request about the basket, which does run in the preview, so a failure is real; it bounds
// the search so a cause that isn't in the theme ends in a question, not a spreading series of rewrites.
const CartFeatureNote = "(Platform note: check the cart code — the data-* hook names in the markup match what the " +
	"script looks for, the script is registered after storefront-api.js, and the basket PUT sends the whole item " +
	"list. If those are all correct, stop searching: call propose_changes with no files, say what you checked, and ask " +
	"the merchant exactly what happens when they click.)"

var (
	untestableFeatureRe = regexp.MustCompile(`(?i)\b(?:check ?out|log ?in|sign ?in|sign ?up|register|registration|` +
		`(?:my |customer |user )?account|orders|order history|my order)\b`)
	cartFeatureRe = regexp.MustCompile(`(?i)\b(?:add[- ]to[- ]cart|cart|basket|mini ?cart)\b`)

	// malfunctionRe is a report that something doesn't work; on its own it marks the request as a functionality one.
	malfunctionRe = regexp.MustCompile(`(?i)(?:not working|n'?t work|isn'?t working|stopped working|does ?nothing|` +
		`nothing happens|do(?:es)?n'?t do anything|broken|\berrors?\b|\bfail(?:s|ed|ing)?\b|\bbug\b|n'?t (?:add|update|open|load|show|respond|submit|go)|` +
		`not (?:adding|updating|opening|loading|showing|responding|submitting)|still not|stuck|\bcan'?t\b|\bwon'?t\b)`)
	// fixRe counts as functionality only without a styling cue: "fix the checkout button colour" is a styling request.
	fixRe     = regexp.MustCompile(`(?i)\b(?:fix|repair|debug|make (?:it|them) work)\b`)
	stylingRe = regexp.MustCompile(`(?i)\b(?:bigger|smaller|larger|colou?rs?|font|size|style|styling|restyle|redesign|` +
		`layout|look|spacing|padding|margin|align|alignment|position|rounded|bold|background|border|width|height|icon|text)\b`)
)

// MentionsUntestableFeature reports whether text is a functionality request about checkout, login, registration or
// customer accounts. Styling requests about the same pages don't match.
func MentionsUntestableFeature(text string) bool {
	return untestableFeatureRe.MatchString(text) && isFunctionalityRequest(text)
}

// MentionsCartFeature reports whether text is a functionality request about the cart or basket.
func MentionsCartFeature(text string) bool {
	return cartFeatureRe.MatchString(text) && isFunctionalityRequest(text)
}

func isFunctionalityRequest(text string) bool {
	return malfunctionRe.MatchString(text) || (fixRe.MatchString(text) && !stylingRe.MatchString(text))
}

// followUpRe marks a message that reports the last fix didn't help without naming what it's about.
var followUpRe = regexp.MustCompile(`(?i)\b(?:still|again|same (?:issue|problem|thing)|didn'?t (?:work|fix|help|change)|not fixed|no change|nothing changed)\b`)

// maxFollowUpLookback bounds how far back a follow-up looks: a run of "still not working" replies inherits the request
// they follow, never one from earlier in the conversation.
const maxFollowUpLookback = 3

// FeatureNotes returns the notes for prompt. A bare follow-up ("still not working") inherits them from the request it
// follows; earlier holds the earlier user messages, newest first, and the walk stops at the first one that isn't itself
// a bare follow-up.
func FeatureNotes(prompt string, earlier []string) []string {
	if notes := notesFor(prompt); len(notes) > 0 || !isBareFollowUp(prompt) {
		return notes
	}
	for i, text := range earlier {
		if i == maxFollowUpLookback {
			break
		}
		if notes := notesFor(text); len(notes) > 0 {
			return notes
		}
		if !isBareFollowUp(text) {
			break
		}
	}
	return nil
}

func notesFor(text string) []string {
	var notes []string
	if MentionsUntestableFeature(text) {
		notes = append(notes, UntestableFeatureNote)
	}
	if MentionsCartFeature(text) {
		notes = append(notes, CartFeatureNote)
	}
	return notes
}

func isBareFollowUp(text string) bool {
	return followUpRe.MatchString(text) && isFunctionalityRequest(text) && !stylingRe.MatchString(text)
}

// IsFixTurn reports whether a turn is fixing broken behaviour rather than designing: it carries preview errors, reports
// a malfunction or asks for a fix (not a styling fix), or is a bare follow-up to such a turn. earlier holds the earlier
// user messages, newest first; the walk is the same as FeatureNotes'.
func IsFixTurn(prompt string, earlier []string, hasPreviewErrors bool) bool {
	if hasPreviewErrors {
		return true
	}
	// A bare follow-up takes its kind from the turn it follows; its own words count only with nothing earlier.
	if !isBareFollowUp(prompt) || len(earlier) == 0 {
		return isFixRequest(prompt)
	}
	for i, text := range earlier {
		if i == maxFollowUpLookback {
			break
		}
		if isFixRequest(text) && !isBareFollowUp(text) {
			return true
		}
		if !isBareFollowUp(text) {
			break
		}
	}
	return false
}

func isFixRequest(text string) bool {
	return isFunctionalityRequest(text) && !stylingRe.MatchString(text)
}

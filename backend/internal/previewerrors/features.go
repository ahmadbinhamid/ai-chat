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

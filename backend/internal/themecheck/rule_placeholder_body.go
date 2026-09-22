package themecheck

import (
	"fmt"
	"regexp"
	"strings"
)

const ruleIDPlaceholderBody = "placeholder-body"

// placeholderBodyRe matches a stripped page body that's just a placeholder marker (e.g. "placeholder", "todo") rather than real content.
var placeholderBodyRe = regexp.MustCompile(`(?i)^(placeholder|lorem( ipsum)?|todo|tbd|to be (determined|decided|filled( in)?)|fill (this|me) in|your (content|text) here|coming soon|\.{3,}|n/?a|xxx+)$`)

// outputExpressionRe matches a {{ }} dynamic binding; its presence alone counts as real content, since a placeholder stub would never reference live data.
var outputExpressionRe = regexp.MustCompile(`(?s)\{\{.*?\}\}`)

// liquidOrHTMLTagRe strips {% %} and <> tags; applied only after outputExpressionRe is checked, so {{ }} bindings aren't mistaken for empty content.
var liquidOrHTMLTagRe = regexp.MustCompile(`(?s)\{%.*?%\}|<[^>]*>`)

// minPriorSignalForShrinkCheck / maxShrinkRatio flag an update whose contentSignal drops below maxShrinkRatio of a
// previous file's signal >= minPriorSignalForShrinkCheck — catches destructive edits matching no known placeholder phrase.
const minPriorSignalForShrinkCheck = 100
const maxShrinkRatio = 0.35

// renderSignalWeight/bindingSignalWeight let a component- or binding-driven page register a substantial contentSignal
// without static prose, so the shrink comparison stays meaningful for it too.
const renderSignalWeight = 200
const bindingSignalWeight = 50

// contentSignal is a rough measure of real content: stripped prose length plus credit for non-layout renders and
// {{ }} bindings. Used only for the shrink comparison above.
func contentSignal(content string) int {
	prose := strings.TrimSpace(strings.Join(strings.Fields(liquidOrHTMLTagRe.ReplaceAllString(content, " ")), " "))
	signal := len(prose)
	signal += renderSignalWeight * nonLayoutRenderCount(content)
	if outputExpressionRe.MatchString(content) {
		signal += bindingSignalWeight
	}
	return signal
}

// checkPlaceholderBody flags a pages/*.liquid file with no real content (prose, a {{ }} binding, or a component render), or a drastic shrink of prior content.
// No minimum-length heuristic — length can't distinguish a stub from terse real content; the shrink check catches that instead.
func checkPlaceholderBody(p Proposal, snap Snapshot) []Finding {
	var findings []Finding
	for _, f := range p.Files {
		if !isPagesLiquidFile(f.Path) {
			continue
		}

		if prev, ok := snap.Files[f.Path]; f.Action == "update" && ok {
			prevSignal := contentSignal(prev)
			if prevSignal >= minPriorSignalForShrinkCheck {
				newSignal := contentSignal(f.Content)
				if float64(newSignal) < float64(prevSignal)*maxShrinkRatio {
					findings = append(findings, Finding{
						Path:     f.Path,
						Rule:     ruleIDPlaceholderBody,
						Severity: SeverityError,
						Message: fmt.Sprintf(
							"this update drastically shrinks %s's real content (roughly %d%% of what was there before) — "+
								"looks like real content may have been replaced with a stub rather than edited. If a large "+
								"removal is genuinely intended, keep the rest of the page's real content and say so "+
								"explicitly in your summary; otherwise rewrite this file preserving its existing content "+
								"plus the requested change.",
							f.Path, newSignal*100/prevSignal),
					})
					continue
				}
			}
		}

		if nonLayoutRenderCount(f.Content) > 0 || outputExpressionRe.MatchString(f.Content) {
			continue
		}

		body := strings.TrimSpace(strings.Join(strings.Fields(liquidOrHTMLTagRe.ReplaceAllString(f.Content, " ")), " "))
		if body == "" || placeholderBodyRe.MatchString(body) {
			findings = append(findings, Finding{
				Path:     f.Path,
				Rule:     ruleIDPlaceholderBody,
				Severity: SeverityError,
				Message: fmt.Sprintf(
					// body is always short here — every branch reaching this Sprintf is an exact placeholder match or empty.
					"the page body looks like placeholder/stub content (%q) rather than real content answering the merchant's "+
						"request — write actual content (prose, or renders of real existing components), and if the request is "+
						"too vague to know what content to write, use needs_clarification instead of guessing.",
					body),
			})
		}
	}
	return findings
}

// nonLayoutRenderCount counts renders besides the mandatory layout wrapper — real component renders count as real content.
func nonLayoutRenderCount(content string) int {
	count := 0
	for _, t := range ScanTags(content) {
		if t.Name != "render" {
			continue
		}
		target, _, ok := ParseRenderTag(t.Raw)
		if ok && target != "liquid/layout-start" && target != "liquid/layout-end" {
			count++
		}
	}
	return count
}

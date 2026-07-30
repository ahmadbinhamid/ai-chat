package themecheck

import (
	"fmt"
	"regexp"
	"strings"
)

const ruleIDPlaceholderBody = "placeholder-body"

// placeholderBodyRe matches a page body that is — once every Liquid/HTML
// tag is stripped out — just a placeholder marker rather than real content.
// Observed in practice: a vague merchant request ("make it better") caused
// the model to overwrite an existing page's real content with the literal
// word "placeholder", passing checkPageBoilerplate cleanly (the required
// renders were still there) since that rule only checks the wrapper, never
// what's inside it.
var placeholderBodyRe = regexp.MustCompile(`(?i)^(placeholder|lorem( ipsum)?|todo|tbd|to be (determined|decided|filled( in)?)|fill (this|me) in|your (content|text) here|coming soon|\.{3,}|n/?a|xxx+)$`)

// outputExpressionRe matches a {{ ... }} Liquid output expression — a
// dynamic binding like {{ product.name }}. Its mere presence is evidence of
// real, page-specific content (there's no legitimate reason a placeholder
// stub would reference live data), so a page containing one is never
// flagged by this rule regardless of how little static prose surrounds it.
var outputExpressionRe = regexp.MustCompile(`(?s)\{\{.*?\}\}`)

// liquidOrHTMLTagRe strips {% ... %} Liquid tags and <...> HTML tags —
// applied only after outputExpressionRe has already been checked for, so a
// page's {{ }} bindings are never mistaken for "no content" just because
// they're not static text.
var liquidOrHTMLTagRe = regexp.MustCompile(`(?s)\{%.*?%\}|<[^>]*>`)

// checkPlaceholderBody enforces: a proposed pages/*.liquid file's actual
// content must be real — literal prose (any non-empty amount; a
// short-but-genuine body like "Sale!" is not this rule's business), a
// dynamic {{ }} binding, or composed from at least one real component/page
// render beyond the mandatory layout wrapper. A file satisfying none of
// those is either a fully empty stub or, per the case this rule exists
// for, real content that got silently destroyed and replaced with an
// exact placeholder phrase. Deliberately no minimum-length heuristic
// beyond non-empty: length alone can't distinguish a stub from genuinely
// terse real content, and a wrong guess there means silently blocking a
// legitimate short page.
func checkPlaceholderBody(p Proposal, _ Snapshot) []Finding {
	var findings []Finding
	for _, f := range p.Files {
		if !isPagesLiquidFile(f.Path) {
			continue
		}

		if hasNonLayoutRender(f.Content) || outputExpressionRe.MatchString(f.Content) {
			continue
		}

		body := strings.TrimSpace(strings.Join(strings.Fields(liquidOrHTMLTagRe.ReplaceAllString(f.Content, " ")), " "))
		if body == "" || placeholderBodyRe.MatchString(body) {
			findings = append(findings, Finding{
				Path:     f.Path,
				Rule:     ruleIDPlaceholderBody,
				Severity: SeverityError,
				Message: fmt.Sprintf(
					// body is always short by construction here: every branch
					// that reaches this Sprintf requires len(body) < minPageBodyTextLen
					// or an exact placeholder-phrase match, so no truncation needed.
					"the page body looks like placeholder/stub content (%q) rather than real content answering the merchant's "+
						"request — write actual content (prose, or renders of real existing components), and if the request is "+
						"too vague to know what content to write, use needs_clarification instead of guessing.",
					body),
			})
		}
	}
	return findings
}

// hasNonLayoutRender reports whether content renders anything besides the
// mandatory liquid/layout-start / liquid/layout-end wrapper — a page
// composed of real component renders (even with little or no prose of its
// own) counts as real content, not a placeholder.
func hasNonLayoutRender(content string) bool {
	for _, t := range ScanTags(content) {
		if t.Name != "render" {
			continue
		}
		target, _, ok := ParseRenderTag(t.Raw)
		if ok && target != "liquid/layout-start" && target != "liquid/layout-end" {
			return true
		}
	}
	return false
}

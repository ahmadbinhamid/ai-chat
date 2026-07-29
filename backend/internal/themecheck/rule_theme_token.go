package themecheck

import (
	"fmt"
	"regexp"
	"strings"
)

const ruleIDThemeToken = "theme-token"

// colorProperties is the set of CSS properties rule 8 checks for a raw hex/
// rgb value — narrowly scoped to properties that spec §6/§9 says must come
// from a --theme-*/--layout-* token, not every property that happens to
// accept a color (e.g. not box-shadow, outline).
var colorProperties = map[string]bool{
	"color": true, "background": true, "background-color": true,
	"border-color": true, "fill": true, "stroke": true,
}

var declRe = regexp.MustCompile(`(?m)([a-zA-Z-]+)\s*:\s*([^;{}]+);`)
var varCallRe = regexp.MustCompile(`var\([^)]*\)`)
var hexOrRGBRe = regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b|rgba?\([^)]*\)`)
var themeVarNoFallbackRe = regexp.MustCompile(`var\(\s*(--(?:theme|layout)-[a-zA-Z0-9_-]+)\s*\)`)

// checkThemeToken enforces rule 8: a raw hex/rgb value used directly as a
// color/background/background-color/border-color/fill/stroke value (outside
// any var() call) is an error; the same inside a --* custom property
// declaration is only a warning (§9 permits component-local tokens with
// literal values). Every var(--theme-*)/var(--layout-*) reference must carry
// a fallback.
func checkThemeToken(p Proposal, _ Snapshot) []Finding {
	var findings []Finding
	for _, f := range p.Files {
		if !strings.HasSuffix(f.Path, ".css") {
			continue
		}

		for _, m := range themeVarNoFallbackRe.FindAllStringSubmatchIndex(f.Content, -1) {
			token := f.Content[m[2]:m[3]]
			findings = append(findings, themeTokenFinding(f.Path, SeverityError, fmt.Sprintf(
				"line %d: var(%s) has no fallback — every var(--theme-*)/var(--layout-*) reference needs one, "+
					"e.g. var(%s, #1e3a8a).", lineAt(f.Content, m[0]), token, token)))
		}

		for _, m := range declRe.FindAllStringSubmatchIndex(f.Content, -1) {
			prop := strings.ToLower(strings.TrimSpace(f.Content[m[2]:m[3]]))
			value := f.Content[m[4]:m[5]]
			line := lineAt(f.Content, m[0])

			switch {
			case colorProperties[prop]:
				withoutVarCalls := varCallRe.ReplaceAllString(value, "")
				if hexOrRGBRe.MatchString(withoutVarCalls) {
					findings = append(findings, themeTokenFinding(f.Path, SeverityError, fmt.Sprintf(
						"line %d: '%s' has a raw color value (%s) — use var(--theme-<key>, <fallback>) instead of "+
							"hardcoding it (§6/§9).", line, prop, strings.TrimSpace(value))))
				}
			case strings.HasPrefix(prop, "--"):
				if hexOrRGBRe.MatchString(value) {
					findings = append(findings, themeTokenFinding(f.Path, SeverityWarning, fmt.Sprintf(
						"line %d: custom property '%s' bakes in a raw color value (%s) — fine for a component-local "+
							"token (§9), but prefer sourcing it from a --theme-*/--layout-* value if one already exists.",
						line, prop, strings.TrimSpace(value))))
				}
			}
		}
	}
	return findings
}

func themeTokenFinding(path string, severity Severity, message string) Finding {
	return Finding{Path: path, Rule: ruleIDThemeToken, Severity: severity, Message: message}
}

package themecheck

import (
	"fmt"
	"regexp"
	"strings"
)

const ruleIDThemeToken = "theme-token"

// colorProperties are properties spec §6/§9 requires a --theme-*/--layout-* token for, not every color-accepting property (e.g. not box-shadow).
var colorProperties = map[string]bool{
	"color": true, "background": true, "background-color": true,
	"border-color": true, "fill": true, "stroke": true,
}

var declRe = regexp.MustCompile(`(?m)([a-zA-Z-]+)\s*:\s*([^;{}]+);`)
var varCallRe = regexp.MustCompile(`var\([^)]*\)`)
var hexOrRGBRe = regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b|rgba?\([^)]*\)`)
var themeVarNoFallbackRe = regexp.MustCompile(`var\(\s*(--(?:theme|layout)-[a-zA-Z0-9_-]+)\s*\)`)

// checkThemeToken enforces rule 8: a raw color outside var() is an error, inside a --* custom property just a warning (§9); every var() reference needs a fallback.
// Declarations byte-identical to pre-edit content are grandfathered, so editing one line of an old theme file doesn't reject the whole file.
func checkThemeToken(p Proposal, snap Snapshot) []Finding {
	var findings []Finding
	for _, f := range p.Files {
		if !strings.HasSuffix(f.Path, ".css") {
			continue
		}

		var prevContent string
		if f.Action == "update" {
			prevContent = snap.Files[f.Path]
		}
		prevDecls := normalizedMatches(prevContent, declRe)
		prevVarRefs := normalizedMatches(prevContent, themeVarNoFallbackRe)

		for _, m := range themeVarNoFallbackRe.FindAllStringSubmatchIndex(f.Content, -1) {
			token := f.Content[m[2]:m[3]]
			if prevVarRefs[strings.TrimSpace(f.Content[m[0]:m[1]])] {
				continue
			}
			line := lineAt(f.Content, m[0])
			findings = append(findings, themeTokenFinding(f.Path, SeverityError, line, m[0], fmt.Sprintf(
				"line %d: var(%s) has no fallback — every var(--theme-*)/var(--layout-*) reference needs one, "+
					"e.g. var(%s, #1e3a8a).", line, token, token)))
		}

		for _, m := range declRe.FindAllStringSubmatchIndex(f.Content, -1) {
			if prevDecls[strings.TrimSpace(f.Content[m[0]:m[1]])] {
				continue
			}
			prop := strings.ToLower(strings.TrimSpace(f.Content[m[2]:m[3]]))
			value := f.Content[m[4]:m[5]]
			line := lineAt(f.Content, m[0])

			switch {
			case colorProperties[prop]:
				withoutVarCalls := varCallRe.ReplaceAllString(value, "")
				if hexOrRGBRe.MatchString(withoutVarCalls) {
					findings = append(findings, themeTokenFinding(f.Path, SeverityError, line, m[0], fmt.Sprintf(
						"line %d: '%s' has a raw color value (%s) — use var(--theme-<key>, <fallback>) instead of "+
							"hardcoding it (§6/§9).", line, prop, strings.TrimSpace(value))))
				}
			case strings.HasPrefix(prop, "--"):
				if hexOrRGBRe.MatchString(value) {
					findings = append(findings, themeTokenFinding(f.Path, SeverityWarning, line, m[0], fmt.Sprintf(
						"line %d: custom property '%s' bakes in a raw color value (%s) — fine for a component-local "+
							"token (§9), but prefer sourcing it from a --theme-*/--layout-* value if one already exists.",
						line, prop, strings.TrimSpace(value))))
				}
			}
		}
	}
	return findings
}

// normalizedMatches returns re's trimmed matches in content, used to grandfather pre-existing declarations/var-refs.
// Empty content yields an empty set, so nothing is grandfathered for a "create" action.
func normalizedMatches(content string, re *regexp.Regexp) map[string]bool {
	set := make(map[string]bool)
	if content == "" {
		return set
	}
	for _, m := range re.FindAllStringIndex(content, -1) {
		set[strings.TrimSpace(content[m[0]:m[1]])] = true
	}
	return set
}

func themeTokenFinding(path string, severity Severity, line, offset int, message string) Finding {
	return Finding{Path: path, Rule: ruleIDThemeToken, Severity: severity, Message: message, Line: line, Offset: offset}
}

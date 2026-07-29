package themecheck

import (
	"regexp"
	"strings"
)

const ruleIDJSShape = "js-shape"

var iifeOpenRe = regexp.MustCompile(`^\(\s*(?:function\b|async\s+function\b|\([^)]*\)\s*=>|\(\)\s*=>)`)
var iifeCloseRe = regexp.MustCompile(`\)\s*\(\s*\)\s*;?\s*$`)
var rootGuardRe = regexp.MustCompile(`if\s*\(\s*![a-zA-Z_$][\w$]*\s*\)`)
var queriesRootRe = regexp.MustCompile(`\b(?:querySelector|getElementById)\s*\(`)
var classSelectorRe = regexp.MustCompile(`(?:querySelector|querySelectorAll)\(\s*['"]\.|getElementsByClassName\(`)
var dataHookRe = regexp.MustCompile(`\[data-[a-zA-Z-]+|\.dataset\.`)

// checkJSShape enforces rule 11 (warning): each new js/*.js file should be
// IIFE-wrapped, query its root element and return early if absent, and use
// data-* hooks rather than class selectors.
func checkJSShape(p Proposal, _ Snapshot) []Finding {
	var findings []Finding
	for _, f := range p.Files {
		if !jsPathRe.MatchString(f.Path) {
			continue
		}
		trimmed := strings.TrimSpace(f.Content)

		if !iifeOpenRe.MatchString(trimmed) || !iifeCloseRe.MatchString(trimmed) {
			findings = append(findings, jsShapeFinding(f.Path,
				"this file doesn't look IIFE-wrapped — wrap it as (function () { 'use strict'; ... })(); or (() => { ... })(); (§10)."))
		}

		if queriesRootRe.MatchString(f.Content) && !rootGuardRe.MatchString(f.Content) {
			findings = append(findings, jsShapeFinding(f.Path,
				"this file queries an element but doesn't look like it guards against it being absent — add an "+
					"'if (!el) return;' style early-out, so the script is safe to load on every page (§10)."))
		}

		if classSelectorRe.MatchString(f.Content) && !dataHookRe.MatchString(f.Content) {
			findings = append(findings, jsShapeFinding(f.Path,
				"this file selects elements by CSS class — use a data-<component>-<purpose> attribute hook instead, "+
					"and keep class selectors for styling only (§10)."))
		}
	}
	return findings
}

func jsShapeFinding(path, message string) Finding {
	return Finding{Path: path, Rule: ruleIDJSShape, Severity: SeverityWarning, Message: message}
}

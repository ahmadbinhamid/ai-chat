package themecheck

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

const ruleIDNoFramework = "no-framework"

// frameworkSignal is one narrow, high-precision pattern for a specific
// framework/library — narrow on purpose: rule 10 is a blocking error, and
// this theme's own approved vocabulary already includes generic-sounding
// names (btn-primary, container, grid) that a broad keyword match would
// false-positive on.
type frameworkSignal struct {
	name string
	re   *regexp.Regexp
}

var frameworkSignals = []frameworkSignal{
	{"Tailwind", regexp.MustCompile(`class="[^"]*\b(?:sm|md|lg|xl|2xl|hover|focus|active|dark):[a-zA-Z0-9_-]`)},
	{"Tailwind", regexp.MustCompile(`class="[^"]*\b(?:w|h|text|bg|p|m|gap)-\[[^\]]+\]`)},
	{"Tailwind", regexp.MustCompile(`(?i)\btailwind\b`)},
	{"Bootstrap", regexp.MustCompile(`class="[^"]*\bbtn\s+btn-`)},
	{"Bootstrap", regexp.MustCompile(`\bdata-bs-[a-z]+=`)},
	{"Bootstrap", regexp.MustCompile(`(?i)\bbootstrap\b`)},
	{"React", regexp.MustCompile(`\bReactDOM\b|\bReact\.createElement\b|from ['"]react['"]|import React\b`)},
	{"Vue", regexp.MustCompile(`\bVue\.createApp\b|\bnew Vue\(|[^\w-](?:v-if|v-for|v-model|v-bind|v-on)=`)},
	{"jQuery", regexp.MustCompile(`\bjQuery\(|\$\(document\)\.ready\(|\$\(function\s*\(`)},
}

// buildToolConfigBasenames are build-tool config files that have no reason
// to exist as a theme .js file — the extension whitelist elsewhere only
// blocks non-.liquid/.css/.js files, so a config file smuggled in as "x.js"
// still needs its own check.
var buildToolConfigBasenames = map[string]bool{
	"tailwind.config": true, "webpack.config": true, "vite.config": true,
	"postcss.config": true, "babel.config": true, "rollup.config": true,
}

// externalScriptSrcRe matches a <script src="..."> whose value is an
// absolute or protocol-relative URL — Go's RE2 engine has no negative
// lookahead, so this deliberately only catches the unambiguous "definitely
// off-theme" case rather than trying to also flag "doesn't use asset_url".
var externalScriptSrcRe = regexp.MustCompile(`<script[^>]*\ssrc="(https?://[^"]*|//[^"]*)"`)

// checkNoFramework enforces rule 10: no CSS/JS framework or library, no
// off-theme <script src>, no build-tool config.
func checkNoFramework(p Proposal, _ Snapshot) []Finding {
	var findings []Finding
	for _, f := range p.Files {
		for _, sig := range frameworkSignals {
			if loc := sig.re.FindStringIndex(f.Content); loc != nil {
				line := lineAt(f.Content, loc[0])
				findings = append(findings, noFrameworkFinding(f.Path, line, fmt.Sprintf(
					"line %d looks like %s — this theme is plain Liquid/CSS/vanilla JS only (§1/§9/§10), no frontend "+
						"framework or library.", line, sig.name)))
			}
		}

		if strings.HasSuffix(f.Path, ".js") {
			base := strings.TrimSuffix(path.Base(f.Path), ".js")
			if buildToolConfigBasenames[base] {
				findings = append(findings, noFrameworkFinding(f.Path, 0,
					"this looks like a build-tool config file — this theme has no build step (§10: vanilla JS only, no bundler)."))
			}
		}

		if strings.HasSuffix(f.Path, ".liquid") {
			for _, m := range externalScriptSrcRe.FindAllStringSubmatchIndex(f.Content, -1) {
				line := lineAt(f.Content, m[0])
				findings = append(findings, noFrameworkFinding(f.Path, line, fmt.Sprintf(
					"line %d: <script src=\"%s\"> doesn't load from this theme's own assets — every script must be "+
						"'{{ 'js/<name>.js' | asset_url }}', never an external/CDN URL.",
					line, f.Content[m[2]:m[3]])))
			}
		}
	}
	return findings
}

func noFrameworkFinding(path string, line int, message string) Finding {
	return Finding{Path: path, Rule: ruleIDNoFramework, Severity: SeverityError, Message: message, Line: line}
}

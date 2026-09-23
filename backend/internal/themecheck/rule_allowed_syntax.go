package themecheck

import (
	"fmt"
	"strings"
)

const ruleIDAllowedSyntax = "allowed-syntax"

// allowedTags is spec §1's complete tag list for this dialect.
var allowedTags = map[string]bool{
	"render": true,
	"if":     true, "elsif": true, "else": true, "endif": true,
	"for": true, "endfor": true,
	"assign":  true,
	"capture": true, "endcapture": true,
	"comment": true, "endcomment": true,
}

// explicitlyForbiddenTags get a specific rejection message instead of the generic unknown-tag one — real Shopify constructs the model may reach for out of habit.
var explicitlyForbiddenTags = map[string]bool{
	"schema": true, "section": true, "include": true,
	"javascript": true, "stylesheet": true,
}

// allowedFilters mirrors spec §1's filter list by hand — a spec edit needs a matching edit here.
var allowedFilters = map[string]bool{
	"default": true, "asset_url": true, "plus": true,
	"size": true, "slice": true, "strip": true, "upcase": true,
	"money": true, "get_products": true, "escape": true, "strip_html": true, "truncate": true,
	// split isn't its own §1 bullet, but the spec's get_products example requires it.
	"split": true,
}

// checkAllowedSyntax enforces rule 2: only §1's tags and filters may appear in a .liquid file.
// Filters share the Expression list rule 12 (known-fields) also consumes from ScanOutputExpressions.
func checkAllowedSyntax(p Proposal, _ Snapshot) []Finding {
	var findings []Finding
	for _, f := range p.Files {
		if !strings.HasSuffix(f.Path, ".liquid") {
			continue
		}

		for _, t := range ScanTags(f.Content) {
			if allowedTags[t.Name] {
				continue
			}
			if explicitlyForbiddenTags[t.Name] {
				findings = append(findings, syntaxFinding(f.Path, fmt.Sprintf(
					"'{%% %s %%}' is not part of this theme's Liquid dialect — this is a real Shopify theme-editor "+
						"construct, but this engine has no theme-editor/schema layer. Remove it; compose the page from "+
						"'{%% render %%}' calls instead (§1/§2).", t.Name)))
				continue
			}
			findings = append(findings, syntaxFinding(f.Path, fmt.Sprintf(
				"'{%% %s %%}' is not one of this dialect's allowed tags (§1: render, if/elsif/else/endif, for/endfor, "+
					"assign, capture/endcapture, comment/endcomment). Remove or replace it.", t.Name)))
		}

		for _, expr := range ScanOutputExpressions(f.Content) {
			for _, filt := range expr.Filters {
				if allowedFilters[filt] {
					continue
				}
				findings = append(findings, syntaxFinding(f.Path, fmt.Sprintf(
					"filter '%s' (in '{{ %s }}') is not one of this dialect's allowed filters (§1: default, asset_url, "+
						"plus, size, slice, strip, upcase). Remove or replace it.", filt, expr.Raw)))
			}
		}
	}
	return findings
}

func syntaxFinding(path, message string) Finding {
	return Finding{Path: path, Rule: ruleIDAllowedSyntax, Severity: SeverityError, Message: message}
}

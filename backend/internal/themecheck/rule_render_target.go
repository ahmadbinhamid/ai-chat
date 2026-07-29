package themecheck

import (
	"fmt"
	"strings"
)

const ruleIDRenderTargetExists = "render-target-exists"

// checkRenderTargetExists enforces rule 4: every {% render 'x' %} path must
// start with 'liquid/' or 'components/' and resolve to a file that already
// exists in the theme or is itself part of this same proposal (a page can
// legitimately render a component created in the same turn).
func checkRenderTargetExists(p Proposal, snap Snapshot) []Finding {
	var findings []Finding
	for _, f := range p.Files {
		if !strings.HasSuffix(f.Path, ".liquid") {
			continue
		}
		for _, t := range ScanTags(f.Content) {
			if t.Name != "render" {
				continue
			}
			target, _, ok := ParseRenderTag(t.Raw)
			if !ok {
				continue
			}

			if !strings.HasPrefix(target, "liquid/") && !strings.HasPrefix(target, "components/") {
				findings = append(findings, renderTargetFinding(f.Path, fmt.Sprintf(
					"{%% render '%s' %%} on line %d: render path must start with 'liquid/' or 'components/', never a bare name.",
					target, t.Line)))
				continue
			}

			resolved := target
			if !strings.HasSuffix(resolved, ".liquid") {
				resolved += ".liquid"
			}
			if snap.HasPath(resolved) {
				continue
			}
			if _, inProposal := p.fileByPath(resolved); inProposal {
				continue
			}
			findings = append(findings, renderTargetFinding(f.Path, fmt.Sprintf(
				"{%% render '%s' %%} on line %d: '%s' does not exist in the theme and isn't one of the files this "+
					"proposal creates.", target, t.Line, resolved)))
		}
	}
	return findings
}

func renderTargetFinding(path, message string) Finding {
	return Finding{Path: path, Rule: ruleIDRenderTargetExists, Severity: SeverityError, Message: message}
}

package themecheck

import (
	"fmt"
	"regexp"
)

const ruleIDAssetRegistered = "asset-registered"

var cssPathRe = regexp.MustCompile(`^(pages|components)/css/[^/]+\.css$`)
var jsPathRe = regexp.MustCompile(`^js/[^/]+\.js$`)

var linkHrefRe = regexp.MustCompile(`<link[^>]*href="\{\{\s*'([^']+)'\s*\|\s*asset_url\s*\}\}"[^>]*>`)
var scriptSrcRe = regexp.MustCompile(`<script[^>]*src="\{\{\s*'([^']+)'\s*\|\s*asset_url\s*\}\}"[^>]*>`)

// registeredAssetPaths returns every theme-relative asset path referenced
// by a <link href=...> or <script src=...> tag (matching the {{ 'path' |
// asset_url }} shape AddStylesheetLink/AddDeferredScript always produce),
// in source order.
func registeredAssetPaths(content string, re *regexp.Regexp) []string {
	var paths []string
	for _, m := range re.FindAllStringSubmatch(content, -1) {
		paths = append(paths, m[1])
	}
	return paths
}

// storefrontAPIPath is the one script every other new script must sit after
// in load order if it depends on it (§10).
const storefrontAPIPath = "js/storefront-api.js"

// checkAssetRegistered enforces rule 5: every pages/css/*.css or
// components/css/*.css file needs a <link> in layout-start, every js/*.js
// file needs a <script defer> in layout-end — satisfied by either what's
// already in the current theme snapshot or what this turn's proposal adds
// via LayoutLinksToAdd/LayoutScriptsToAdd. New scripts that call
// window.StorefrontApi must be registered after storefront-api.js.
func checkAssetRegistered(p Proposal, snap Snapshot) []Finding {
	var findings []Finding

	finalLinks := append(registeredAssetPaths(snap.LayoutStart(), linkHrefRe), p.LayoutLinksToAdd...)
	linkSet := toSet(finalLinks)

	finalScripts := append(registeredAssetPaths(snap.LayoutEnd(), scriptSrcRe), p.LayoutScriptsToAdd...)
	scriptIndex := make(map[string]int, len(finalScripts))
	for i, path := range finalScripts {
		if _, exists := scriptIndex[path]; !exists {
			scriptIndex[path] = i
		}
	}
	storefrontIdx, hasStorefront := scriptIndex[storefrontAPIPath]

	for _, f := range p.Files {
		switch {
		case cssPathRe.MatchString(f.Path):
			if !linkSet[f.Path] {
				findings = append(findings, assetRegisteredFinding(f.Path, fmt.Sprintf(
					"'%s' has no matching <link rel=\"stylesheet\"> in liquid/layout-start.liquid — either add one there "+
						"or include '%s' in layout_links_to_add.", f.Path, f.Path)))
			}

		case jsPathRe.MatchString(f.Path):
			if _, registered := scriptIndex[f.Path]; !registered {
				findings = append(findings, assetRegisteredFinding(f.Path, fmt.Sprintf(
					"'%s' has no matching <script defer> in liquid/layout-end.liquid — either add one there or include "+
						"'%s' in layout_scripts_to_add.", f.Path, f.Path)))
				continue
			}
			if f.Path == storefrontAPIPath || !usesStorefrontAPI(f.Content) {
				continue
			}
			if !hasStorefront || scriptIndex[f.Path] <= storefrontIdx {
				findings = append(findings, assetRegisteredFinding(f.Path, fmt.Sprintf(
					"'%s' calls window.StorefrontApi but its <script> tag isn't registered after '%s' in the §10 load "+
						"order.", f.Path, storefrontAPIPath)))
			}
		}
	}

	return findings
}

func usesStorefrontAPI(content string) bool {
	return regexp.MustCompile(`\bStorefrontApi\b`).MatchString(content)
}

func toSet(items []string) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, item := range items {
		set[item] = true
	}
	return set
}

func assetRegisteredFinding(path, message string) Finding {
	return Finding{Path: path, Rule: ruleIDAssetRegistered, Severity: SeverityError, Message: message}
}

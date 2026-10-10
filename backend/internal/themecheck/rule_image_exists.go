package themecheck

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const ruleIDImageExists = "image-exists"

// assetImageRe captures a literal images/ path passed to asset_url; a Liquid variable can't be checked and is skipped.
var assetImageRe = regexp.MustCompile(`['"](images/[^'"{}\s]+)['"]\s*\|\s*asset_url`)

// checkImageExists rejects an asset_url image that is neither in the theme (or draft) nor saved by this proposal's
// use_attachments: a made-up file renders as a broken image.
func checkImageExists(p Proposal, snap Snapshot) []Finding {
	var findings []Finding
	for _, f := range p.Files {
		if !strings.HasSuffix(f.Path, ".liquid") && !strings.HasSuffix(f.Path, ".css") {
			continue
		}
		for _, m := range assetImageRe.FindAllStringSubmatchIndex(f.Content, -1) {
			img := f.Content[m[2]:m[3]]
			if snap.HasPath(img) || slices.Contains(p.PlacedImages, img) {
				continue
			}
			line := lineAt(f.Content, m[0])
			findings = append(findings, Finding{
				Path: f.Path, Rule: ruleIDImageExists, Severity: SeverityError, Line: line,
				Message: fmt.Sprintf("line %d: %s isn't in the theme, so it would show as a broken image — use an image "+
					"that exists (see the file tree), save one with use_attachments (an attached image or a "+
					"search_stock_images photo), or use an inline SVG. Never invent an image file.", line, img),
			})
		}
	}
	return findings
}

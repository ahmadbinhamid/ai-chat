package themecheck

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const ruleIDImageHost = "image-host"

var (
	imgTagRe = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	// imgURLAttrRe captures src and srcset values in either quote style.
	imgURLAttrRe = regexp.MustCompile(`(?is)\s(?:src|srcset)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
)

// checkImageHost rejects an <img> hotlinking a host that is neither the platform's nor the stock provider's; theme
// assets and product images reach a page through relative paths or Liquid, so only literal absolute URLs are judged.
func checkImageHost(p Proposal, snap Snapshot) []Finding {
	var findings []Finding
	for _, f := range p.Files {
		if !strings.HasSuffix(f.Path, ".liquid") {
			continue
		}
		for _, tag := range imgTagRe.FindAllStringIndex(f.Content, -1) {
			for _, m := range imgURLAttrRe.FindAllStringSubmatch(f.Content[tag[0]:tag[1]], -1) {
				for _, host := range externalHosts(m[1] + m[2]) {
					if snap.ImageHosts[host] {
						continue
					}
					line := lineAt(f.Content, tag[0])
					findings = append(findings, Finding{
						Path: f.Path, Rule: ruleIDImageHost, Severity: SeverityError, Line: line,
						Message: fmt.Sprintf("line %d: <img> loads from %s — use a theme image (asset_url), product data, "+
							"or a photo returned by search_stock_images; never hotlink another site or invent an image URL.", line, host),
					})
				}
			}
		}
	}
	return findings
}

// externalHosts returns the hosts of the literal absolute URLs in a src or srcset value; Liquid-built URLs are skipped.
func externalHosts(value string) []string {
	var hosts []string
	for _, candidate := range strings.Split(value, ",") {
		fields := strings.Fields(candidate)
		if len(fields) == 0 || strings.Contains(fields[0], "{{") || strings.Contains(fields[0], "{%") {
			continue
		}
		raw := fields[0]
		if !strings.HasPrefix(raw, "//") && !strings.HasPrefix(strings.ToLower(raw), "http://") && !strings.HasPrefix(strings.ToLower(raw), "https://") {
			continue
		}
		if strings.HasPrefix(raw, "//") {
			raw = "https:" + raw
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			hosts = append(hosts, raw)
			continue
		}
		hosts = append(hosts, strings.ToLower(u.Hostname()))
	}
	return hosts
}

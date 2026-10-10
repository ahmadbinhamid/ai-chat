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
	// styleAttrRe captures inline style values, whose url(...) can load an image like an <img> does.
	styleAttrRe = regexp.MustCompile(`(?is)\sstyle\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	// cssURLRe captures a url(...) argument, quoted or bare.
	cssURLRe = regexp.MustCompile(`(?i)url\(\s*(?:"([^"]*)"|'([^']*)'|([^)'"\s]+))\s*\)`)
)

// checkImageHost rejects an image hotlinked from any host but the platform's — in an <img>, a .liquid style attribute
// or a .css url(); stock photos are saved into the theme, and relative paths and Liquid-built URLs are never judged.
func checkImageHost(p Proposal, snap Snapshot) []Finding {
	var findings []Finding
	reject := func(path, content string, offset int, what, host string) {
		if snap.ImageHosts[host] {
			return
		}
		line := lineAt(content, offset)
		findings = append(findings, Finding{
			Path: path, Rule: ruleIDImageHost, Severity: SeverityError, Line: line,
			Message: fmt.Sprintf("line %d: %s loads from %s — use a theme image via asset_url (save a stock photo into "+
				"images/ with use_attachments first) or product data; never hotlink another site or invent an image URL.",
				line, what, host),
		})
	}
	for _, f := range p.Files {
		switch {
		case strings.HasSuffix(f.Path, ".liquid"):
			for _, tag := range imgTagRe.FindAllStringIndex(f.Content, -1) {
				for _, m := range imgURLAttrRe.FindAllStringSubmatch(f.Content[tag[0]:tag[1]], -1) {
					for _, host := range srcsetHosts(m[1] + m[2]) {
						reject(f.Path, f.Content, tag[0], "<img>", host)
					}
				}
			}
			for _, attr := range styleAttrRe.FindAllStringSubmatchIndex(f.Content, -1) {
				value := submatch(f.Content, attr, 1) + submatch(f.Content, attr, 2)
				for _, host := range cssURLHosts(value) {
					reject(f.Path, f.Content, attr[0], "a style attribute's url()", host)
				}
			}
		case strings.HasSuffix(f.Path, ".css"):
			for _, m := range cssURLRe.FindAllStringSubmatchIndex(f.Content, -1) {
				raw := submatch(f.Content, m, 1) + submatch(f.Content, m, 2) + submatch(f.Content, m, 3)
				if host, ok := externalHost(raw); ok {
					reject(f.Path, f.Content, m[0], "CSS url()", host)
				}
			}
		}
	}
	return findings
}

// srcsetHosts returns the hosts of the literal absolute URLs in a src or srcset value.
func srcsetHosts(value string) []string {
	var hosts []string
	for _, candidate := range strings.Split(value, ",") {
		if fields := strings.Fields(candidate); len(fields) > 0 {
			if host, ok := externalHost(fields[0]); ok {
				hosts = append(hosts, host)
			}
		}
	}
	return hosts
}

// cssURLHosts returns the hosts of the literal absolute url(...) arguments in a CSS fragment.
func cssURLHosts(css string) []string {
	var hosts []string
	for _, m := range cssURLRe.FindAllStringSubmatch(css, -1) {
		if host, ok := externalHost(m[1] + m[2] + m[3]); ok {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

// externalHost reports raw's host when it is a literal absolute URL; relative, data: and Liquid-built URLs are skipped.
func externalHost(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "{{") || strings.Contains(raw, "{%") {
		return "", false
	}
	lower := strings.ToLower(raw)
	if !strings.HasPrefix(raw, "//") && !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return "", false
	}
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw, true
	}
	return strings.ToLower(u.Hostname()), true
}

// submatch returns group n of a FindAllStringSubmatchIndex match, or "" when the group didn't take part.
func submatch(s string, m []int, n int) string {
	if m[2*n] < 0 {
		return ""
	}
	return s[m[2*n]:m[2*n+1]]
}

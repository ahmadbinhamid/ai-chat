package themecheck

import (
	"fmt"
	"regexp"
	"strings"
)

const ruleIDSEOFilled = "seo-filled"

var placeholderTextRe = regexp.MustCompile(`(?i)^(\.{3,}|todo|lorem( ipsum)?)$`)

// checkSEOFilled enforces rule 7 (warning): a new page's seo_title,
// seo_description, seo_keywords must be non-empty and not placeholder text.
func checkSEOFilled(p Proposal, _ Snapshot) []Finding {
	entry := p.PageRegistryEntry
	if entry == nil {
		return nil
	}

	var findings []Finding
	fields := []struct {
		name  string
		value string
	}{
		{"seo_title", entry.SEOTitle},
		{"seo_description", entry.SEODescription},
		{"seo_keywords", entry.SEOKeywords},
	}
	for _, field := range fields {
		trimmed := strings.TrimSpace(field.value)
		if trimmed == "" {
			findings = append(findings, seoFilledFinding(fmt.Sprintf(
				"page_registry_entry.%s is empty — write a real, page-specific value.", field.name)))
			continue
		}
		if placeholderTextRe.MatchString(trimmed) {
			findings = append(findings, seoFilledFinding(fmt.Sprintf(
				"page_registry_entry.%s looks like placeholder text (%q) — write a real, page-specific value.",
				field.name, trimmed)))
		}
	}
	return findings
}

func seoFilledFinding(message string) Finding {
	return Finding{Rule: ruleIDSEOFilled, Severity: SeverityWarning, Message: message}
}

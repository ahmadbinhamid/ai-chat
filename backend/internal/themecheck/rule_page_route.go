package themecheck

import (
	"encoding/json"
	"fmt"
	"strings"

	"ai-chat/internal/themefs"
)

const ruleIDPageRoute = "page-route"

// systemPageTypes are the fixed, one-per-type routes spec §5 says already
// exist — a proposal must never register a second entry of one of these.
var systemPageTypes = map[string]bool{
	"home": true, "products": true, "product": true, "categories": true, "category": true,
	"cart": true, "basket": true, "login": true, "register": true, "forget_password": true,
	"reset_password": true, "verify_otp": true, "my_account": true, "my_orders": true,
	"change_password": true,
}

// existingPageEntry is the subset of a pages.json record rule 6 needs to
// check a new registration against.
type existingPageEntry struct {
	Slug string `json:"slug"`
	Type string `json:"type"`
}

// parseExistingPages parses pages.json; a malformed shape is treated as no known routes, not a finding.
func parseExistingPages(pagesJSON string) []existingPageEntry {
	if strings.TrimSpace(pagesJSON) == "" {
		return nil
	}
	var entries []existingPageEntry
	if err := json.Unmarshal([]byte(pagesJSON), &entries); err != nil {
		return nil
	}
	return entries
}

// expectedPageFilePath maps a pages.json entry to its .liquid file per spec §5 ("/pages/auth" -> pages/auth/<page>.liquid).
func expectedPageFilePath(entry *themefs.PageEntry) string {
	if entry.Path == "/pages/auth" {
		return "pages/auth/" + entry.Page + ".liquid"
	}
	return "pages/" + entry.Page + ".liquid"
}

// checkPageRoute enforces rule 6: a new pages/<slug>.liquid file needs a matching pages.json registration
// (page == slug == basename, no duplicate slug or system-type entry).
func checkPageRoute(p Proposal, snap Snapshot) []Finding {
	var findings []Finding
	entry := p.PageRegistryEntry

	for _, f := range p.Files {
		if f.Action != "create" || !isPagesLiquidFile(f.Path) {
			continue
		}
		if entry == nil || expectedPageFilePath(entry) != f.Path {
			findings = append(findings, pageRouteFinding(f.Path,
				"this new page file has no matching pages.json registration (page_registry_entry) — every new "+
					"pages/*.liquid file needs one, with page/slug equal to its basename."))
		}
	}

	if entry == nil {
		return findings
	}

	if entry.Page == "" {
		findings = append(findings, pageRouteFinding("", "page_registry_entry.page must not be empty."))
	}
	if entry.Type == "custom" && entry.Slug != entry.Page {
		findings = append(findings, pageRouteFinding("", fmt.Sprintf(
			"page_registry_entry: slug (%q) must equal page (%q) for a custom page.", entry.Slug, entry.Page)))
	}

	existing := parseExistingPages(snap.PagesJSON())
	for _, e := range existing {
		if e.Slug == entry.Slug {
			findings = append(findings, pageRouteFinding("", fmt.Sprintf(
				"page_registry_entry: slug %q is already taken by an existing route.", entry.Slug)))
			break
		}
	}
	if systemPageTypes[entry.Type] {
		for _, e := range existing {
			if e.Type == entry.Type {
				findings = append(findings, pageRouteFinding("", fmt.Sprintf(
					"page_registry_entry: type %q is a system route type that already has an entry — never register a "+
						"second one.", entry.Type)))
				break
			}
		}
	}

	return findings
}

func pageRouteFinding(path, message string) Finding {
	return Finding{Path: path, Rule: ruleIDPageRoute, Severity: SeverityError, Message: message}
}

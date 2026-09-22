package themecheck

const ruleIDPageRequiresAuth = "page-requires-auth"

// checkPageRequiresAuth rejects a proposal setting page_registry_entry.requires_auth. The model can no longer emit
// this field, but themefs.PageEntry still carries it (mirrors pages.json), so this is a cheap safety net.
func checkPageRequiresAuth(p Proposal, _ Snapshot) []Finding {
	if p.PageRegistryEntry == nil || !p.PageRegistryEntry.RequiresAuth {
		return nil
	}
	return []Finding{{
		Rule:     ruleIDPageRequiresAuth,
		Severity: SeverityError,
		Message: "page_registry_entry.requires_auth must not be set — every page ai-chat can create is type " +
			"\"custom\" and never needs it (§5), and flowpos-backend doesn't forward this field today regardless.",
	}}
}

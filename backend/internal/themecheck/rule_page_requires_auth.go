package themecheck

const ruleIDPageRequiresAuth = "page-requires-auth"

// checkPageRequiresAuth rejects any proposal that sets
// page_registry_entry.requires_auth. resultSchema no longer lets the model
// emit this field at all (see internal/ai/generator.go's resultSchema
// comment: per spec §5, requires_auth only applies to the fixed
// my_account/my_orders/change_password system routes ai-chat can never
// register, and — separately — flowpos-backend doesn't forward it from this
// API today regardless). themefs.PageEntry still carries the field because
// it mirrors the full pages.json record shape, so it remains assignable in
// Go even though nothing should ever set it; this rule is the cheap
// insurance that catches it if something does.
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

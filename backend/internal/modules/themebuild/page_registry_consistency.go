package themebuild

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"ai-chat/internal/ai"
	"ai-chat/internal/themefs"
)

// validatePageFileRegistryConsistency checks staged create/update/delete
// proposals against the current pages.json so file ↔ registry never diverge:
//   - new page liquid ⇒ matching registry add (page_registry_entry / merge)
//   - delete page liquid ⇒ matching registry removal when pages.json is updated
//   - no unrelated registry removals on register/create
//   - no duplicate identities
//   - pages.json remains valid JSON when present in the proposal
func validatePageFileRegistryConsistency(currentPagesJSON string, result *ai.Result, expectedNewIDs []string) error {
	if result == nil {
		return nil
	}

	beforeRaws, err := parsePagesJSONRaw(currentPagesJSON)
	if err != nil {
		return fmt.Errorf("pages.json consistency: current registry invalid: %w", err)
	}
	beforeIDs := map[string]bool{}
	for _, raw := range beforeRaws {
		if id := identityFromRawPage(raw); id != "" {
			if beforeIDs[id] {
				return fmt.Errorf("pages.json consistency: duplicate identity %q in current registry", id)
			}
			beforeIDs[id] = true
		}
	}

	proposedPagesJSON := ""
	createdPageIDs := map[string]bool{}
	deletedPageIDs := map[string]bool{}
	updatedPageIDs := map[string]bool{}

	for _, f := range result.Files {
		low := strings.ToLower(strings.TrimSpace(f.Path))
		act := strings.ToLower(strings.TrimSpace(f.Action))
		if low == "pages.json" {
			if act == "update" || act == "create" {
				proposedPagesJSON = f.Content
			}
			continue
		}
		if !strings.HasPrefix(low, "pages/") || !strings.HasSuffix(low, ".liquid") || strings.HasPrefix(low, "pages/css/") {
			continue
		}
		id := pageIDFromLiquidPath(f.Path)
		if id == "" {
			continue
		}
		switch act {
		case "create":
			createdPageIDs[id] = true
		case "delete":
			deletedPageIDs[id] = true
		case "update":
			updatedPageIDs[id] = true
		}
	}

	registryAdds := map[string]bool{}
	if result.PageRegistryEntry != nil {
		id := pageEntryIdentity(normalizeRegistryEntry(result.PageRegistryEntry))
		if id == "" {
			return fmt.Errorf("pages.json consistency: page_registry_entry missing page/slug")
		}
		registryAdds[id] = true
	}
	expected := map[string]bool{}
	for _, id := range expectedNewIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		expected[id] = true
		registryAdds[id] = true
	}

	var afterIDs map[string]bool
	if strings.TrimSpace(proposedPagesJSON) != "" {
		afterRaws, err := parsePagesJSONRaw(proposedPagesJSON)
		if err != nil {
			return fmt.Errorf("pages.json consistency: proposed pages.json is not valid JSON: %w", err)
		}
		afterIDs = map[string]bool{}
		seen := map[string]bool{}
		for _, raw := range afterRaws {
			id := identityFromRawPage(raw)
			if id == "" {
				continue
			}
			if seen[id] {
				return fmt.Errorf("pages.json consistency: duplicate identity %q in proposed registry", id)
			}
			seen[id] = true
			afterIDs[id] = true
			if !beforeIDs[id] {
				registryAdds[id] = true
			}
		}
	}

	for id := range createdPageIDs {
		if registryAdds[id] || expected[id] || (afterIDs != nil && afterIDs[id]) {
			continue
		}
		return fmt.Errorf("pages.json consistency: created page %q has no registry entry (use page_registry_entry)", id)
	}

	for id := range registryAdds {
		if createdPageIDs[id] || updatedPageIDs[id] || beforeIDs[id] || expected[id] {
			continue
		}
		if afterIDs != nil {
			return fmt.Errorf("pages.json consistency: registry add %q has no matching page file create/update", id)
		}
	}

	if afterIDs != nil {
		for id := range deletedPageIDs {
			if afterIDs[id] {
				return fmt.Errorf("pages.json consistency: deleted page %q still has a registry entry", id)
			}
		}
		for id := range beforeIDs {
			if afterIDs[id] || deletedPageIDs[id] {
				continue
			}
			return fmt.Errorf("pages.json consistency: unrelated registry entry %q was removed", id)
		}
		if len(deletedPageIDs) == 0 {
			if err := validatePagesJSONNonDestructive(currentPagesJSON, proposedPagesJSON, mapKeys(registryAdds), true); err != nil {
				return fmt.Errorf("pages.json consistency: %w", err)
			}
		}
	}
	return nil
}

func pageIDFromLiquidPath(relPath string) string {
	low := strings.ToLower(strings.TrimSpace(relPath))
	if !strings.HasPrefix(low, "pages/") || !strings.HasSuffix(low, ".liquid") || strings.HasPrefix(low, "pages/css/") {
		return ""
	}
	return strings.TrimSpace(strings.TrimSuffix(path.Base(low), ".liquid"))
}

func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// removePageRegistryIdentities drops only the listed identities from pages.json,
// preserving every other entry's exact JSON bytes.
func removePageRegistryIdentities(current string, removeIDs []string) (string, error) {
	raws, err := parsePagesJSONRaw(current)
	if err != nil {
		return "", err
	}
	drop := map[string]bool{}
	for _, id := range removeIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			drop[id] = true
		}
	}
	kept := make([]json.RawMessage, 0, len(raws))
	for _, raw := range raws {
		id := identityFromRawPage(raw)
		if id != "" && drop[id] {
			continue
		}
		kept = append(kept, raw)
	}
	out := serializePagesJSON(kept, current)
	if !json.Valid([]byte(strings.TrimSpace(out))) {
		return "", fmt.Errorf("serialize pages.json: produced invalid JSON")
	}
	return out, nil
}

// ensureCreateHasRegistry rejects page-file creates that omit registration.
func ensureCreateHasRegistry(prompt string, result *ai.Result) error {
	if result == nil || result.NeedsClarification || result.AnsweredQuestion {
		return nil
	}
	if isMultiPageCreatePrompt(prompt) {
		return incompleteMultiPageCreateProposal(prompt, result)
	}
	return ensureProposedCreatesRegistered(result)
}

func ensureProposedCreatesRegistered(result *ai.Result) error {
	if result == nil {
		return nil
	}
	hasPagesJSON := false
	creates := make([]string, 0, 2)
	seen := map[string]bool{}
	for _, f := range result.Files {
		low := strings.ToLower(strings.TrimSpace(f.Path))
		act := strings.ToLower(strings.TrimSpace(f.Action))
		if low == "pages.json" && (act == "update" || act == "create") {
			hasPagesJSON = true
		}
		if act != "create" {
			continue
		}
		id := pageIDFromLiquidPath(f.Path)
		if id == "" || id == "home" {
			continue
		}
		if !seen[id] {
			seen[id] = true
			creates = append(creates, id)
		}
	}
	if len(creates) == 0 {
		return nil
	}
	// page_registry_entry can only cover ONE create. Multiple creates without
	// pages.json is the N05 failure mode (pair of service pages → thrash).
	if len(creates) > 1 && !hasPagesJSON {
		return fmt.Errorf("incomplete page create: %d new pages (%s) need a pages.json update or compound atomic steps — page_registry_entry only registers one page",
			len(creates), strings.Join(creates, ", "))
	}
	if !hasPagesJSON && result.PageRegistryEntry == nil {
		return fmt.Errorf("incomplete page create: %s need page_registry_entry (or pages.json merge) — file without registry is not allowed",
			strings.Join(creates, ", "))
	}
	if result.PageRegistryEntry != nil && len(creates) == 1 {
		regID := pageEntryIdentity(normalizeRegistryEntry(result.PageRegistryEntry))
		if regID != "" && !strings.EqualFold(regID, creates[0]) {
			return fmt.Errorf("page_registry_entry identity %q does not match created page %q", regID, creates[0])
		}
	}
	return nil
}

// synthesizeMissingPageRegistry fills page_registry_entry when the model
// created exactly one pages/<slug>.liquid and forgot registration. Avoids
// burning repair budget on a deterministic omit. Multi-create is left to
// compound / pages.json (see ensureProposedCreatesRegistered).
func synthesizeMissingPageRegistry(result *ai.Result) bool {
	if result == nil || result.PageRegistryEntry != nil {
		return false
	}
	hasPagesJSON := false
	var createPath, createID string
	createCount := 0
	for _, f := range result.Files {
		low := strings.ToLower(strings.TrimSpace(f.Path))
		act := strings.ToLower(strings.TrimSpace(f.Action))
		if low == "pages.json" && (act == "update" || act == "create") {
			hasPagesJSON = true
		}
		if act != "create" {
			continue
		}
		id := pageIDFromLiquidPath(f.Path)
		if id == "" || id == "home" {
			continue
		}
		createCount++
		createPath = strings.TrimSpace(f.Path)
		createID = id
	}
	if hasPagesJSON || createCount != 1 || createID == "" {
		return false
	}
	title := humanizePageSlug(createID)
	pathPrefix := "/pages"
	if strings.Contains(strings.ToLower(createPath), "/auth/") {
		pathPrefix = "/pages/auth"
	}
	result.PageRegistryEntry = &themefs.PageEntry{
		Title:  title,
		Slug:   createID,
		Page:   createID,
		Path:   pathPrefix,
		Type:   "custom",
		Status: "published",
	}
	return true
}

func humanizePageSlug(slug string) string {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return "Page"
	}
	parts := strings.Split(slug, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

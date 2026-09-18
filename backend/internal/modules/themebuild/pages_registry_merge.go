package themebuild

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"ai-chat/internal/ai"
	"ai-chat/internal/themefs"
)

// pageRegistryIdentity is the stable key used to match pages.json rows
// (page field, else slug — same convention as themecheck / FlowPOS).
func pageRegistryIdentity(page, slug string) string {
	page = strings.TrimSpace(page)
	if page != "" {
		return page
	}
	return strings.TrimSpace(slug)
}

func pageEntryIdentity(e *themefs.PageEntry) string {
	if e == nil {
		return ""
	}
	return pageRegistryIdentity(e.Page, e.Slug)
}

func normalizeRegistryEntry(entry *themefs.PageEntry) *themefs.PageEntry {
	if entry == nil {
		return nil
	}
	cp := *entry
	if strings.TrimSpace(cp.Page) == "" {
		cp.Page = strings.TrimSpace(cp.Slug)
	}
	if strings.TrimSpace(cp.Slug) == "" {
		cp.Slug = strings.TrimSpace(cp.Page)
	}
	if strings.TrimSpace(cp.Type) == "" {
		cp.Type = "custom"
	}
	if strings.TrimSpace(cp.Path) == "" {
		cp.Path = "/pages"
	}
	cp.Status = normalizePageRegistryStatus(&cp)
	return &cp
}

// parsePagesJSONRaw keeps each existing entry's exact JSON bytes so a merge
// can append without rewriting unrelated metadata/formatting.
func parsePagesJSONRaw(pagesJSON string) ([]json.RawMessage, error) {
	trimmed := strings.TrimSpace(pagesJSON)
	if trimmed == "" {
		return nil, nil
	}
	var raws []json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &raws); err != nil {
		return nil, fmt.Errorf("parse pages.json: %w", err)
	}
	return raws, nil
}

func identityFromRawPage(raw json.RawMessage) string {
	var e struct {
		Page string `json:"page"`
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return ""
	}
	return pageRegistryIdentity(e.Page, e.Slug)
}

func pagesJSONIndent(pagesJSON string) string {
	if i := strings.Index(pagesJSON, "\n"); i >= 0 && i+1 < len(pagesJSON) {
		j := i + 1
		for j < len(pagesJSON) && (pagesJSON[j] == ' ' || pagesJSON[j] == '\t') {
			j++
		}
		if j > i+1 {
			return pagesJSON[i+1 : j]
		}
	}
	return "  "
}

// mergePageRegistryEntries upserts entries into current pages.json.
// Existing entry bytes are preserved; new entries are appended. Duplicate
// identities update in place (no second row).
func mergePageRegistryEntries(current string, entries []*themefs.PageEntry) (string, []string, error) {
	raws, err := parsePagesJSONRaw(current)
	if err != nil {
		return "", nil, err
	}
	byID := make(map[string]int, len(raws))
	for i, raw := range raws {
		id := identityFromRawPage(raw)
		if id == "" {
			continue
		}
		byID[id] = i
	}

	added := make([]string, 0, len(entries))
	for _, entry := range entries {
		norm := normalizeRegistryEntry(entry)
		if norm == nil {
			continue
		}
		id := pageEntryIdentity(norm)
		if id == "" {
			return "", nil, fmt.Errorf("page_registry_entry missing page/slug identity")
		}
		encoded, err := json.Marshal(norm)
		if err != nil {
			return "", nil, fmt.Errorf("encode page_registry_entry %q: %w", id, err)
		}
		if idx, ok := byID[id]; ok {
			raws[idx] = json.RawMessage(encoded)
			continue
		}
		byID[id] = len(raws)
		raws = append(raws, json.RawMessage(encoded))
		added = append(added, id)
	}

	indent := pagesJSONIndent(current)
	var buf bytes.Buffer
	buf.WriteByte('[')
	if len(raws) > 0 {
		buf.WriteByte('\n')
		for i, raw := range raws {
			buf.WriteString(indent)
			buf.Write(raw)
			if i < len(raws)-1 {
				buf.WriteByte(',')
			}
			buf.WriteByte('\n')
		}
	}
	buf.WriteByte(']')
	buf.WriteByte('\n')
	return buf.String(), added, nil
}

// validatePagesJSONNonDestructive ensures every pre-merge identity still
// exists. When restrictNew is true, only expectedNew identities may be added.
func validatePagesJSONNonDestructive(before, after string, expectedNew []string, restrictNew bool) error {
	beforeRaws, err := parsePagesJSONRaw(before)
	if err != nil {
		return fmt.Errorf("destructive pages.json check (before): %w", err)
	}
	afterRaws, err := parsePagesJSONRaw(after)
	if err != nil {
		return fmt.Errorf("destructive pages.json check (after): %w", err)
	}

	beforeIDs := make(map[string]bool, len(beforeRaws))
	for _, raw := range beforeRaws {
		if id := identityFromRawPage(raw); id != "" {
			beforeIDs[id] = true
		}
	}
	afterIDs := make(map[string]bool, len(afterRaws))
	for _, raw := range afterRaws {
		if id := identityFromRawPage(raw); id != "" {
			afterIDs[id] = true
		}
	}

	var missing []string
	for id := range beforeIDs {
		if !afterIDs[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("destructive pages.json replacement rejected: missing %d existing page(s) including %q",
			len(missing), missing[0])
	}

	if restrictNew {
		allowedNew := make(map[string]bool, len(expectedNew))
		for _, id := range expectedNew {
			allowedNew[strings.TrimSpace(id)] = true
		}
		var unexpected []string
		for id := range afterIDs {
			if beforeIDs[id] {
				continue
			}
			if !allowedNew[id] {
				unexpected = append(unexpected, id)
			}
		}
		if len(unexpected) > 0 {
			return fmt.Errorf("destructive pages.json replacement rejected: unexpected new page(s) %q", unexpected[0])
		}
	}
	if len(afterRaws) < len(beforeRaws) {
		return fmt.Errorf("destructive pages.json replacement rejected: entry count shrank %d → %d",
			len(beforeRaws), len(afterRaws))
	}
	return nil
}

func isDestructivePagesJSONProposal(current, proposed string) error {
	if strings.TrimSpace(proposed) == "" {
		return fmt.Errorf("empty pages.json proposal")
	}
	return validatePagesJSONNonDestructive(current, proposed, nil, false)
}

// stripModelPagesJSON removes files[] pages.json entries so compound create
// never stages a model full-file rewrite (truncated excerpt → −500 line churn).
func stripModelPagesJSON(result *ai.Result) (*ai.Result, bool) {
	if result == nil || len(result.Files) == 0 {
		return result, false
	}
	out := cloneResultFiles(result)
	kept := make([]ai.GeneratedFile, 0, len(out.Files))
	stripped := false
	for _, f := range out.Files {
		if strings.EqualFold(strings.TrimSpace(f.Path), "pages.json") {
			stripped = true
			continue
		}
		kept = append(kept, f)
	}
	out.Files = kept
	return out, stripped
}

// recoverRegistryFromPagesJSONProposal extracts a single new registry entry
// when the model wrote pages.json instead of page_registry_entry — only if
// every existing identity is kept and exactly one identity is added.
func recoverRegistryFromPagesJSONProposal(current, proposed string) (*themefs.PageEntry, error) {
	if err := isDestructivePagesJSONProposal(current, proposed); err != nil {
		return nil, err
	}
	beforeRaws, err := parsePagesJSONRaw(current)
	if err != nil {
		return nil, err
	}
	afterRaws, err := parsePagesJSONRaw(proposed)
	if err != nil {
		return nil, err
	}
	beforeIDs := map[string]bool{}
	for _, raw := range beforeRaws {
		if id := identityFromRawPage(raw); id != "" {
			beforeIDs[id] = true
		}
	}
	var newcomers []themefs.PageEntry
	for _, raw := range afterRaws {
		id := identityFromRawPage(raw)
		if id == "" || beforeIDs[id] {
			continue
		}
		var e themefs.PageEntry
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		newcomers = append(newcomers, e)
	}
	if len(newcomers) != 1 {
		return nil, fmt.Errorf("pages.json proposal added %d pages (want exactly 1 for atomic recovery)", len(newcomers))
	}
	return normalizeRegistryEntry(&newcomers[0]), nil
}

// injectMergedPagesJSON sets/replaces result.Files pages.json with a
// deterministic merge of current + registries (never model content).
func injectMergedPagesJSON(result *ai.Result, current string, registries []*themefs.PageEntry) error {
	if result == nil {
		return fmt.Errorf("nil result")
	}
	if len(registries) == 0 {
		return nil
	}
	merged, added, err := mergePageRegistryEntries(current, registries)
	if err != nil {
		return err
	}
	if err := validatePagesJSONNonDestructive(current, merged, added, true); err != nil {
		return err
	}
	replaced := false
	for i := range result.Files {
		if strings.EqualFold(strings.TrimSpace(result.Files[i].Path), "pages.json") {
			result.Files[i] = ai.GeneratedFile{Path: "pages.json", Action: "update", Content: merged}
			replaced = true
			break
		}
	}
	if !replaced {
		result.Files = append(result.Files, ai.GeneratedFile{
			Path: "pages.json", Action: "update", Content: merged,
		})
	}
	return nil
}

// prepareCompoundCreateStep strips model pages.json, recovers registry if
// needed, and returns the cleaned step result.
func prepareCompoundCreateStep(result *ai.Result, currentPagesJSON string) (*ai.Result, error) {
	if result == nil {
		return nil, fmt.Errorf("empty proposal")
	}
	var modelPagesJSON string
	for _, f := range result.Files {
		if strings.EqualFold(strings.TrimSpace(f.Path), "pages.json") {
			modelPagesJSON = f.Content
			break
		}
	}
	cleaned, stripped := stripModelPagesJSON(result)
	if cleaned.PageRegistryEntry == nil && stripped && strings.TrimSpace(modelPagesJSON) != "" {
		recovered, err := recoverRegistryFromPagesJSONProposal(currentPagesJSON, modelPagesJSON)
		if err != nil {
			return cleaned, fmt.Errorf("proposal/tool contract mismatch: model pages.json rewrite rejected (%v); use page_registry_entry only", err)
		}
		cleaned.PageRegistryEntry = recovered
	}
	return cleaned, nil
}

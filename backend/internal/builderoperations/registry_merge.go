package builderoperations

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"ai-chat/internal/themefs"
)

const pathPagesJSON = "pages.json"

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

func normalizeRegistryStatus(entry *themefs.PageEntry) string {
	if entry == nil {
		return "published"
	}
	status := strings.TrimSpace(strings.ToLower(entry.Status))
	page := strings.TrimSpace(strings.ToLower(entry.Page))
	slug := strings.TrimSpace(strings.ToLower(entry.Slug))
	typ := strings.TrimSpace(strings.ToLower(entry.Type))
	if page == "home" || slug == "home" || typ == "home" {
		return "published"
	}
	if status == "" || status == "published" {
		return "published"
	}
	return status
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
	cp.Status = normalizeRegistryStatus(&cp)
	return &cp
}

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

	for id := range beforeIDs {
		if !afterIDs[id] {
			return fmt.Errorf("destructive pages.json replacement rejected: missing existing page %q", id)
		}
	}

	if restrictNew {
		allowedNew := make(map[string]bool, len(expectedNew))
		for _, id := range expectedNew {
			allowedNew[strings.TrimSpace(id)] = true
		}
		for id := range afterIDs {
			if beforeIDs[id] {
				continue
			}
			if !allowedNew[id] {
				return fmt.Errorf("destructive pages.json replacement rejected: unexpected new page %q", id)
			}
		}
	}
	if len(afterRaws) < len(beforeRaws) {
		return fmt.Errorf("destructive pages.json replacement rejected: entry count shrank")
	}
	return nil
}

type pagesJSONRow struct {
	Title  string `json:"title"`
	Slug   string `json:"slug"`
	Type   string `json:"type"`
	Page   string `json:"page"`
	Path   string `json:"path"`
	Status string `json:"status"`
}

func parsePagesJSONRows(raw string) ([]pagesJSONRow, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var arr []pagesJSONRow
	if err := json.Unmarshal([]byte(raw), &arr); err == nil {
		return arr, nil
	}
	var wrap map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &wrap); err != nil {
		return nil, err
	}
	for _, key := range []string{"pages", "data", "items"} {
		if v, ok := wrap[key]; ok {
			if err := json.Unmarshal(v, &arr); err == nil {
				return arr, nil
			}
		}
	}
	return nil, fmt.Errorf("unexpected pages.json shape")
}

func rowLiquidPath(r pagesJSONRow) string {
	page := strings.TrimSpace(r.Page)
	if page == "" {
		page = strings.TrimSpace(r.Slug)
	}
	if page == "" {
		return ""
	}
	if strings.Contains(strings.ToLower(r.Path), "auth") {
		return "pages/auth/" + page + ".liquid"
	}
	return "pages/" + page + ".liquid"
}

func registeredLiquidPaths(rows []pagesJSONRow) map[string]bool {
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		if p := rowLiquidPath(r); p != "" {
			out[strings.ToLower(p)] = true
		}
	}
	return out
}

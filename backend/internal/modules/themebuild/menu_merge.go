package themebuild

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"ai-chat/internal/ai"
	"ai-chat/internal/themefs"
)

// Canonical live nav lives at defaults.json → menu.items[]
// (theme_engine_spec.md §6): id, label, url, children[], optional pageId.
const canonicalMenuIdentity = "menu"

// AddToMenuOperation is the structured atomic nav mutation. The model (or
// compound create progress) supplies identities/labels; the backend merges
// into the store-backed defaults.json — never a full model rewrite.
type AddToMenuOperation struct {
	PageIdentity string // pages.json page/slug
	MenuIdentity string // always "menu" for current theme schema
	Label        string
	URL          string
	ItemID       string // menu.items[].id
	PageID       string // optional menu.items[].pageId
	Position     string // "append" (default) — matches existing storefront order
}

// menuItemSchema is the on-disk shape of one defaults.json menu.items entry.
type menuItemSchema struct {
	ID       string          `json:"id"`
	Label    string          `json:"label"`
	URL      string          `json:"url"`
	Children json.RawMessage `json:"children"`
	PageID   string          `json:"pageId,omitempty"`
}

func looksLikeTruncatedDefaultsJSON(s string) bool {
	low := strings.ToLower(s)
	return strings.Contains(s, "…(truncated") ||
		strings.Contains(low, "...(truncated") ||
		strings.Contains(low, "(truncated for simple-edit)")
}

func defaultsJSONIndent(defaultsJSON string) string {
	if i := strings.Index(defaultsJSON, "\n"); i >= 0 && i+1 < len(defaultsJSON) {
		j := i + 1
		for j < len(defaultsJSON) && (defaultsJSON[j] == ' ' || defaultsJSON[j] == '\t') {
			j++
		}
		if j > i+1 {
			return defaultsJSON[i+1 : j]
		}
	}
	return "  "
}

// parseDefaultsRoot keeps each top-level key's exact JSON bytes so a menu
// merge can rewrite only menu without inventing unrelated config.
func parseDefaultsRoot(defaultsJSON string) (map[string]json.RawMessage, []string, error) {
	trimmed := strings.TrimSpace(defaultsJSON)
	if trimmed == "" {
		return nil, nil, fmt.Errorf("parse defaults.json: empty")
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	tok, err := dec.Token()
	if err != nil {
		return nil, nil, fmt.Errorf("parse defaults.json: %w", err)
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return nil, nil, fmt.Errorf("parse defaults.json: root must be an object")
	}
	root := map[string]json.RawMessage{}
	order := make([]string, 0, 16)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, nil, fmt.Errorf("parse defaults.json: %w", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, nil, fmt.Errorf("parse defaults.json: expected object key")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, nil, fmt.Errorf("parse defaults.json key %q: %w", key, err)
		}
		root[key] = raw
		order = append(order, key)
	}
	if _, err := dec.Token(); err != nil {
		return nil, nil, fmt.Errorf("parse defaults.json: %w", err)
	}
	return root, order, nil
}

func isValidDefaultsJSON(s string) bool {
	if looksLikeTruncatedDefaultsJSON(s) {
		return false
	}
	_, _, err := parseDefaultsRoot(s)
	return err == nil
}

func menuItemsFromRoot(root map[string]json.RawMessage) ([]json.RawMessage, error) {
	menuRaw, ok := root[canonicalMenuIdentity]
	if !ok {
		return nil, fmt.Errorf("menu operation: target menu %q does not exist", canonicalMenuIdentity)
	}
	var menu map[string]json.RawMessage
	if err := json.Unmarshal(menuRaw, &menu); err != nil {
		return nil, fmt.Errorf("menu operation: parse menu: %w", err)
	}
	itemsRaw, ok := menu["items"]
	if !ok {
		return nil, fmt.Errorf("menu operation: menu.items missing")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(itemsRaw, &items); err != nil {
		return nil, fmt.Errorf("menu operation: parse menu.items: %w", err)
	}
	return items, nil
}

func menuItemIdentity(raw json.RawMessage) (id, pageID, url, label string) {
	var it struct {
		ID     string `json:"id"`
		PageID string `json:"pageId"`
		URL    string `json:"url"`
		Label  string `json:"label"`
	}
	_ = json.Unmarshal(raw, &it)
	return strings.TrimSpace(it.ID), strings.TrimSpace(it.PageID), strings.TrimSpace(it.URL), strings.TrimSpace(it.Label)
}

func serializeDefaultsJSON(root map[string]json.RawMessage, keyOrder []string, indentHint string) (string, error) {
	indent := defaultsJSONIndent(indentHint)
	seen := map[string]bool{}
	keys := make([]string, 0, len(root))
	for _, k := range keyOrder {
		if _, ok := root[k]; ok {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	for k := range root {
		if !seen[k] {
			keys = append(keys, k)
		}
	}

	var buf bytes.Buffer
	buf.WriteByte('{')
	buf.WriteByte('\n')
	for i, k := range keys {
		buf.WriteString(indent)
		keyJSON, err := json.Marshal(k)
		if err != nil {
			return "", err
		}
		buf.Write(keyJSON)
		buf.WriteString(": ")
		raw := bytes.TrimSpace(root[k])
		if len(raw) == 0 {
			return "", fmt.Errorf("serialize defaults.json: empty value for %q", k)
		}
		// Preserve unrelated keys byte-for-byte. Only re-indent the menu we
		// rewrote so the rest of defaults.json stays a non-destructive merge.
		if k == canonicalMenuIdentity {
			var pretty bytes.Buffer
			if err := json.Indent(&pretty, raw, indent, indent); err != nil {
				buf.Write(raw)
			} else {
				buf.Write(pretty.Bytes())
			}
		} else {
			buf.Write(raw)
		}
		if i < len(keys)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteByte('}')
	buf.WriteByte('\n')
	out := buf.String()
	if !json.Valid([]byte(strings.TrimSpace(out))) {
		return "", fmt.Errorf("serialize defaults.json: produced invalid JSON")
	}
	return out, nil
}

// resolveCanonicalDefaultsJSON picks the merge/staging base from ThemeStore.
// Never trusts a truncated ThemeContext prompt stub or model full rewrite.
func resolveCanonicalDefaultsJSON(storeDefaultsJSON, promptHint string) (string, error) {
	var candidates []string
	if !looksLikeTruncatedDefaultsJSON(storeDefaultsJSON) {
		candidates = append(candidates, storeDefaultsJSON)
	}
	if strings.TrimSpace(promptHint) != "" && !looksLikeTruncatedDefaultsJSON(promptHint) {
		candidates = append(candidates, promptHint)
	}
	for _, c := range candidates {
		if isValidDefaultsJSON(c) {
			return c, nil
		}
	}
	if strings.TrimSpace(storeDefaultsJSON) != "" && !looksLikeTruncatedDefaultsJSON(storeDefaultsJSON) {
		_, _, err := parseDefaultsRoot(storeDefaultsJSON)
		return "", fmt.Errorf("menu operation: current defaults.json invalid: %w", err)
	}
	if looksLikeTruncatedDefaultsJSON(storeDefaultsJSON) || looksLikeTruncatedDefaultsJSON(promptHint) {
		return "", fmt.Errorf("menu operation: current defaults.json invalid: truncated prompt stub is not a merge base")
	}
	return "", fmt.Errorf("menu operation: current defaults.json invalid: empty")
}

func normalizeAddToMenuOperation(op AddToMenuOperation) (AddToMenuOperation, error) {
	op.PageIdentity = strings.TrimSpace(op.PageIdentity)
	op.MenuIdentity = strings.TrimSpace(op.MenuIdentity)
	if op.MenuIdentity == "" {
		op.MenuIdentity = canonicalMenuIdentity
	}
	if op.MenuIdentity != canonicalMenuIdentity {
		return op, fmt.Errorf("menu operation: unsupported menu identity %q", op.MenuIdentity)
	}
	if op.PageIdentity == "" {
		return op, fmt.Errorf("menu operation: page identity required")
	}
	op.ItemID = strings.TrimSpace(op.ItemID)
	if op.ItemID == "" {
		op.ItemID = op.PageIdentity
	}
	op.Label = strings.TrimSpace(op.Label)
	if op.Label == "" {
		op.Label = humanizeMenuLabel(op.PageIdentity)
	}
	op.URL = strings.TrimSpace(op.URL)
	if op.URL == "" {
		op.URL = "/" + strings.Trim(op.PageIdentity, "/")
	}
	if !strings.HasPrefix(op.URL, "/") {
		op.URL = "/" + op.URL
	}
	op.PageID = strings.TrimSpace(op.PageID)
	if op.PageID == "" {
		op.PageID = op.PageIdentity
	}
	op.Position = strings.ToLower(strings.TrimSpace(op.Position))
	if op.Position == "" {
		op.Position = "append"
	}
	if op.Position != "append" {
		return op, fmt.Errorf("menu operation: unsupported position %q", op.Position)
	}
	return op, nil
}

func humanizeMenuLabel(slug string) string {
	parts := strings.FieldsFunc(slug, func(r rune) bool {
		return r == '-' || r == '_' || r == ' '
	})
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
	}
	out := strings.TrimSpace(strings.Join(parts, " "))
	if out == "" {
		return slug
	}
	return out
}

// addToMenuOperationFromPageEntry builds the atomic op from a create-step
// registry entry (compound workflow — no DeepSeek).
func addToMenuOperationFromPageEntry(entry *themefs.PageEntry) (AddToMenuOperation, error) {
	norm := normalizeRegistryEntry(entry)
	if norm == nil {
		return AddToMenuOperation{}, fmt.Errorf("menu operation: nil page registry entry")
	}
	id := pageEntryIdentity(norm)
	if id == "" {
		return AddToMenuOperation{}, fmt.Errorf("menu operation: page identity required")
	}
	label := strings.TrimSpace(norm.Title)
	if label == "" {
		label = humanizeMenuLabel(id)
	}
	return normalizeAddToMenuOperation(AddToMenuOperation{
		PageIdentity: id,
		MenuIdentity: canonicalMenuIdentity,
		Label:        label,
		URL:          "/" + strings.Trim(id, "/"),
		ItemID:       id,
		PageID:       id,
		Position:     "append",
	})
}

func menuItemAlreadyPresent(items []json.RawMessage, op AddToMenuOperation) bool {
	wantID := strings.ToLower(op.ItemID)
	wantPage := strings.ToLower(op.PageIdentity)
	wantURL := strings.ToLower(op.URL)
	for _, raw := range items {
		id, pageID, url, _ := menuItemIdentity(raw)
		if id != "" && strings.ToLower(id) == wantID {
			return true
		}
		if pageID != "" && strings.ToLower(pageID) == wantPage {
			return true
		}
		if url != "" && strings.ToLower(url) == wantURL {
			return true
		}
	}
	return false
}

func encodeMenuItem(op AddToMenuOperation) (json.RawMessage, error) {
	it := menuItemSchema{
		ID:       op.ItemID,
		Label:    op.Label,
		URL:      op.URL,
		Children: json.RawMessage("[]"),
		PageID:   op.PageID,
	}
	encoded, err := json.Marshal(it)
	if err != nil {
		return nil, fmt.Errorf("menu operation: encode item: %w", err)
	}
	if !json.Valid(encoded) {
		return nil, fmt.Errorf("menu operation: encode item produced invalid JSON")
	}
	return json.RawMessage(encoded), nil
}

// mergeAddToMenuOperation appends one nav item onto current defaults.json.
// Existing menu entries and unrelated top-level keys are preserved.
// Duplicate page/menu entries are no-ops (idempotent).
func mergeAddToMenuOperation(current string, op AddToMenuOperation) (string, bool, error) {
	if looksLikeTruncatedDefaultsJSON(current) {
		return "", false, fmt.Errorf("menu operation: truncated prompt stub is not a merge base")
	}
	op, err := normalizeAddToMenuOperation(op)
	if err != nil {
		return "", false, err
	}
	root, keyOrder, err := parseDefaultsRoot(current)
	if err != nil {
		return "", false, fmt.Errorf("menu operation: current defaults.json invalid: %w", err)
	}
	items, err := menuItemsFromRoot(root)
	if err != nil {
		return "", false, err
	}
	if menuItemAlreadyPresent(items, op) {
		return current, false, nil
	}
	encoded, err := encodeMenuItem(op)
	if err != nil {
		return "", false, err
	}
	items = append(items, encoded)

	itemsJSON, err := json.Marshal(items)
	if err != nil {
		return "", false, fmt.Errorf("menu operation: encode items: %w", err)
	}
	menuRaw, ok := root[canonicalMenuIdentity]
	if !ok {
		return "", false, fmt.Errorf("menu operation: target menu %q does not exist", canonicalMenuIdentity)
	}
	var menu map[string]json.RawMessage
	if err := json.Unmarshal(menuRaw, &menu); err != nil {
		return "", false, fmt.Errorf("menu operation: parse menu: %w", err)
	}
	menu["items"] = json.RawMessage(itemsJSON)
	newMenu, err := json.Marshal(menu)
	if err != nil {
		return "", false, fmt.Errorf("menu operation: encode menu: %w", err)
	}
	root[canonicalMenuIdentity] = json.RawMessage(newMenu)

	out, err := serializeDefaultsJSON(root, keyOrder, current)
	if err != nil {
		return "", false, err
	}
	return out, true, nil
}

// validateMenuMergeInvariants checks merge output before checkpoint/stage.
func validateMenuMergeInvariants(before, after string, op AddToMenuOperation) error {
	if !json.Valid([]byte(strings.TrimSpace(after))) {
		return fmt.Errorf("menu operation: proposed defaults.json is not valid JSON")
	}
	beforeRoot, _, err := parseDefaultsRoot(before)
	if err != nil {
		return fmt.Errorf("menu operation: current defaults.json invalid: %w", err)
	}
	afterRoot, _, err := parseDefaultsRoot(after)
	if err != nil {
		return fmt.Errorf("menu operation: proposed defaults.json invalid: %w", err)
	}
	// Unrelated top-level keys must be preserved byte-for-byte where possible;
	// at minimum every pre-merge key must still exist.
	for k, beforeVal := range beforeRoot {
		if k == canonicalMenuIdentity {
			continue
		}
		afterVal, ok := afterRoot[k]
		if !ok {
			return fmt.Errorf("menu operation: unrelated key %q removed", k)
		}
		if !bytes.Equal(bytes.TrimSpace(beforeVal), bytes.TrimSpace(afterVal)) {
			return fmt.Errorf("menu operation: unrelated key %q changed", k)
		}
	}
	beforeItems, err := menuItemsFromRoot(beforeRoot)
	if err != nil {
		return err
	}
	afterItems, err := menuItemsFromRoot(afterRoot)
	if err != nil {
		return err
	}
	// Existing menu identities preserved (by item id).
	beforeIDs := map[string]bool{}
	for _, raw := range beforeItems {
		id, _, _, _ := menuItemIdentity(raw)
		if id != "" {
			beforeIDs[strings.ToLower(id)] = true
		}
	}
	afterIDs := map[string]bool{}
	for _, raw := range afterItems {
		id, pageID, url, label := menuItemIdentity(raw)
		if id == "" || label == "" || url == "" {
			return fmt.Errorf("menu operation: menu item missing required fields (id/label/url)")
		}
		low := strings.ToLower(id)
		if afterIDs[low] {
			return fmt.Errorf("menu operation: duplicate menu item %q", id)
		}
		afterIDs[low] = true
		_ = pageID
	}
	for id := range beforeIDs {
		if !afterIDs[id] {
			return fmt.Errorf("menu operation: existing menu item %q removed", id)
		}
	}
	op, err = normalizeAddToMenuOperation(op)
	if err != nil {
		return err
	}
	if !menuItemAlreadyPresent(afterItems, op) {
		return fmt.Errorf("menu operation: page %q not present in menu after merge", op.PageIdentity)
	}
	// Only expected addition: at most one new item.
	if len(afterItems) < len(beforeItems) || len(afterItems) > len(beforeItems)+1 {
		return fmt.Errorf("menu operation: unexpected menu size change %d → %d", len(beforeItems), len(afterItems))
	}
	return nil
}

// applyAddToMenuCheckpoint merges one structured op onto a validated
// defaults.json checkpoint. On failure the previous checkpoint is unchanged.
func applyAddToMenuCheckpoint(checkpoint string, op AddToMenuOperation) (next string, added bool, err error) {
	next, added, err = mergeAddToMenuOperation(checkpoint, op)
	if err != nil {
		return checkpoint, false, err
	}
	if err := validateMenuMergeInvariants(checkpoint, next, op); err != nil {
		return checkpoint, false, err
	}
	return next, added, nil
}

// stripModelDefaultsJSON removes model-authored defaults.json so compound
// create never stages a full DeepSeek rewrite of theme config.
func stripModelDefaultsJSON(result *ai.Result) (*ai.Result, bool) {
	if result == nil {
		return result, false
	}
	kept := make([]ai.GeneratedFile, 0, len(result.Files))
	stripped := false
	for _, f := range result.Files {
		if strings.EqualFold(strings.TrimSpace(f.Path), "defaults.json") {
			stripped = true
			continue
		}
		kept = append(kept, f)
	}
	if !stripped {
		return result, false
	}
	out := *result
	out.Files = kept
	return &out, true
}

// injectCheckpointDefaultsJSON writes an already-validated defaults.json body.
func injectCheckpointDefaultsJSON(result *ai.Result, defaultsJSON string) error {
	if result == nil {
		return fmt.Errorf("nil result")
	}
	if !isValidDefaultsJSON(defaultsJSON) {
		return fmt.Errorf("menu operation: checkpoint defaults.json is not valid JSON")
	}
	replaced := false
	for i := range result.Files {
		if strings.EqualFold(strings.TrimSpace(result.Files[i].Path), "defaults.json") {
			result.Files[i] = ai.GeneratedFile{Path: "defaults.json", Action: "update", Content: defaultsJSON}
			replaced = true
			break
		}
	}
	if !replaced {
		result.Files = append(result.Files, ai.GeneratedFile{
			Path: "defaults.json", Action: "update", Content: defaultsJSON,
		})
	}
	return nil
}

// runDeterministicAddToMenu applies one AddToMenuOperation per registry entry
// against store-backed defaults.json. DeepSeek is never called.
func runDeterministicAddToMenu(baseDefaultsJSON string, registries []*themefs.PageEntry) (defaultsJSON string, summary string, err error) {
	if len(registries) == 0 {
		return "", "", fmt.Errorf("menu operation: no page identities to add")
	}
	base, err := resolveCanonicalDefaultsJSON(baseDefaultsJSON, "")
	if err != nil {
		return "", "", err
	}
	cp := base
	addedLabels := make([]string, 0, len(registries))
	for _, entry := range registries {
		op, opErr := addToMenuOperationFromPageEntry(entry)
		if opErr != nil {
			return cp, "", opErr
		}
		next, added, mergeErr := applyAddToMenuCheckpoint(cp, op)
		if mergeErr != nil {
			return cp, "", mergeErr
		}
		cp = next
		if added {
			addedLabels = append(addedLabels, op.Label)
		}
	}
	if len(addedLabels) == 0 {
		summary = "Navigation already included the new pages."
	} else {
		summary = "Added to navigation: " + strings.Join(addedLabels, ", ") + "."
	}
	return cp, summary, nil
}

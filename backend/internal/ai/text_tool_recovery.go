package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

// decodeProposeInput is the one decoder for propose_changes input, shared by real tool_use calls and text-recovered ones.
func decodeProposeInput(raw json.RawMessage) (Result, error) {
	var result Result
	if err := json.Unmarshal(normalizeStringEncodedFields(raw), &result); err != nil {
		return Result{}, err
	}
	return result, nil
}

// structuredProposeFields are typed as objects or arrays; some models send them as strings instead: "" for none, or
// the JSON itself encoded as text.
var structuredProposeFields = []string{"files", "page_registry_entry", "layout_links_to_add", "layout_scripts_to_add", "use_attachments"}

// normalizeStringEncodedFields turns those string forms back into what they stand for; anything else is left for
// Unmarshal to reject, so a genuinely wrong value still fails.
func normalizeStringEncodedFields(raw json.RawMessage) json.RawMessage {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return raw
	}
	changed := false
	for _, key := range structuredProposeFields {
		var s string
		if v, ok := obj[key]; !ok || json.Unmarshal(v, &s) != nil {
			continue
		}
		t := strings.TrimSpace(s)
		switch {
		case t == "" || t == "null":
			obj[key] = json.RawMessage("null")
			changed = true
		case (strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")) && json.Valid([]byte(t)):
			obj[key] = json.RawMessage(t)
			changed = true
		case key == "page_registry_entry":
			// Safe to drop: a created page with no entry gets one synthesized (synthesizeMissingPageRegistry).
			slog.Warn("ai: dropped an unreadable page_registry_entry string", "value_head", headRunes(t, 120))
			obj[key] = json.RawMessage("null")
			changed = true
		}
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return out
}

// malformedProposeMessage goes back to the model as the propose_changes tool result, so it can resend the call.
func malformedProposeMessage(err error) string {
	return fmt.Sprintf("Your propose_changes input could not be read: %s. Call propose_changes again with the same "+
		"changes and the documented types: files and use_attachments are arrays, page_registry_entry is an object or "+
		"null, and none of them is ever a string.", err)
}

// recoverProposeFromText recovers a propose_changes call DeepSeek wrote as text. Strict: only the first tool-call-shaped
// object counts, it must be propose_changes, and its arguments must match resultSchema's keys and decode; else false.
func recoverProposeFromText(message anthropic.Message) (json.RawMessage, bool) {
	var b strings.Builder
	for _, block := range message.Content {
		if block.Type == "text" {
			b.WriteString(block.Text)
			b.WriteString("\n")
		}
	}
	text := b.String()

	for i := 0; i < len(text); {
		start := strings.IndexByte(text[i:], '{')
		if start < 0 {
			return nil, false
		}
		start += i
		// Only `{"` can open a keyed object; skipping the rest keeps CSS/prose braces cheap.
		if !strings.HasPrefix(strings.TrimLeft(text[start+1:], " \t\r\n"), `"`) {
			i = start + 1
			continue
		}
		// Escaping per candidate, not over the whole reply, so an unmatched quote in prose can't flip string state.
		candidate := escapeControlCharsInStrings(text[start:])
		dec := json.NewDecoder(strings.NewReader(candidate))
		var obj map[string]json.RawMessage
		if err := dec.Decode(&obj); err != nil {
			i = start + 1
			continue
		}
		// Skip past the whole object, so a call nested inside unrelated JSON never counts as one.
		i = start + objectEnd(text[start:])

		rawName, hasName := obj["name"]
		args, hasArgs := obj["arguments"]
		if !hasName || !hasArgs {
			continue
		}
		var name string
		if err := json.Unmarshal(rawName, &name); err != nil || name != toolNameProposeChanges {
			return nil, false
		}
		if !matchesProposeSchemaKeys(args) {
			return nil, false
		}
		if _, err := decodeProposeInput(args); err != nil {
			return nil, false
		}
		return args, true
	}
	return nil, false
}

// objectEnd returns the length of the JSON object at the start of s by matching braces outside strings. Escaping only
// changes bytes inside strings, so this matches what the decoder consumed from the escaped copy.
func objectEnd(s string) int {
	depth, inString, escaped := 0, false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			escaped = false
		case inString && c == '\\':
			escaped = true
		case c == '"':
			inString = !inString
		case !inString && c == '{':
			depth++
		case !inString && c == '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(s)
}

// escapeControlCharsInStrings escapes raw newlines/tabs that appear inside JSON string literals (an LLM text-reply quirk
// strict JSON rejects). Only characters inside strings change, so the structure the parser sees is untouched.
func escapeControlCharsInStrings(s string) string {
	var out strings.Builder
	inString, escaped := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			escaped = false
		case inString && c == '\\':
			escaped = true
		case c == '"':
			inString = !inString
		case inString && c == '\n':
			out.WriteString(`\n`)
			continue
		case inString && c == '\r':
			out.WriteString(`\r`)
			continue
		case inString && c == '\t':
			out.WriteString(`\t`)
			continue
		}
		out.WriteByte(c)
	}
	return out.String()
}

// matchesProposeSchemaKeys applies resultSchema's top-level contract: an object with every required key and no unknown one.
func matchesProposeSchemaKeys(args json.RawMessage) bool {
	if !bytes.HasPrefix(bytes.TrimSpace(args), []byte("{")) {
		return false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil {
		return false
	}
	properties, _ := resultSchema["properties"].(map[string]any)
	for key := range obj {
		if _, ok := properties[key]; !ok {
			return false
		}
	}
	for _, key := range resultSchema["required"].([]string) {
		if _, ok := obj[key]; !ok {
			return false
		}
	}
	return true
}

// recoveredMaterializeFailure is sent as a plain user message: a text-recovered call has no tool_use ID to pair a tool_result with.
func recoveredMaterializeFailure(retryMsg string) string {
	return fmt.Sprintf("ERROR: your propose_changes call (written as text, not a tool call) could not be applied: %s "+
		"Call the propose_changes tool again with the corrected input.", retryMsg)
}

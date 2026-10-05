package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

// decodeProposeInput is the one decoder for propose_changes input, shared by real tool_use calls and text-recovered ones.
func decodeProposeInput(raw json.RawMessage) (Result, error) {
	var result Result
	if err := json.Unmarshal(raw, &result); err != nil {
		return Result{}, err
	}
	return result, nil
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

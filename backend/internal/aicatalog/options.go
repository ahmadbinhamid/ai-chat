package aicatalog

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
)

// optionKeyRe keeps option keys plain, since the generator sets each one as a JSON path in the request body.
var optionKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// reservedOptionKeys are request fields the generator sets itself; an option overriding one would bypass the
// catalogue's own thinking/effort/tool handling.
var reservedOptionKeys = map[string]bool{
	"model": true, "messages": true, "system": true, "max_tokens": true, "tools": true, "tool_choice": true,
	"thinking": true, "output_config": true, "stream": true, "metadata": true, "temperature": true,
	"top_p": true, "top_k": true, "stop_sequences": true,
}

// parseOptions decodes an options value: absent is no options, otherwise a JSON object of extra request-body fields.
func parseOptions(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return nil, fmt.Errorf("options must be a JSON object")
	}
	for k := range out {
		if !optionKeyRe.MatchString(k) {
			return nil, fmt.Errorf("option %q: keys must be lowercase letters, digits and underscores", k)
		}
		if reservedOptionKeys[k] {
			return nil, fmt.Errorf("option %q is set by the generator and can't be overridden", k)
		}
	}
	return out, nil
}

// mergeOptions deep-merges over onto base, over winning; nested objects merge key by key, anything else is replaced.
// Neither input is modified.
func mergeOptions(base, over map[string]any) map[string]any {
	if len(base) == 0 && len(over) == 0 {
		return nil
	}
	out := maps.Clone(base)
	if out == nil {
		out = map[string]any{}
	}
	for k, v := range over {
		bv, bok := out[k].(map[string]any)
		ov, ook := v.(map[string]any)
		if bok && ook {
			out[k] = mergeOptions(bv, ov)
			continue
		}
		out[k] = v
	}
	return out
}

// RequestFields is the extra request-body fields for model id: its provider's options deep-merged with its own.
// Nil when there are none. Callers must not modify the result.
func (c *Catalog) RequestFields(id string) map[string]any {
	return c.fields[id]
}

func (c *Catalog) buildRequestFields() error {
	providerOpts := make(map[string]map[string]any, len(c.Providers))
	for name, p := range c.Providers {
		opts, err := parseOptions(p.Options)
		if err != nil {
			return fmt.Errorf("provider %q: %w", name, err)
		}
		providerOpts[name] = opts
	}
	c.fields = make(map[string]map[string]any, len(c.Models))
	for _, m := range c.Models {
		opts, err := parseOptions(m.Options)
		if err != nil {
			return fmt.Errorf("model %q: %w", m.ID, err)
		}
		if merged := mergeOptions(providerOpts[m.Provider], opts); len(merged) > 0 {
			c.fields[m.ID] = merged
		}
	}
	return nil
}

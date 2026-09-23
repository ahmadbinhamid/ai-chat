package liquidrender

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// scope is one template's (or partial's) variable bindings, kept reflection-free.
type scope map[string]any

var literalKeywords = map[string]any{
	"true": true, "false": false, "nil": nil, "null": nil, "blank": "", "empty": "",
}

var numberRe = regexp.MustCompile(`^-?\d+(\.\d+)?$`)

// evalValue evaluates a single value expression (no filters): string, number, keyword, or path.
func evalValue(raw string, vars scope) any {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return nil
	case raw[0] == '\'' || raw[0] == '"':
		if len(raw) >= 2 && raw[len(raw)-1] == raw[0] {
			return raw[1 : len(raw)-1]
		}
		return raw
	case numberRe.MatchString(raw):
		if f, err := strconv.ParseFloat(raw, 64); err == nil {
			return f
		}
		return raw
	default:
		if v, ok := literalKeywords[raw]; ok {
			return v
		}
		return resolvePath(raw, vars)
	}
}

// resolvePath resolves a dotted path ("product.choices") against vars, returning nil for any
// unknown root or field — matches Liquid's "undefined renders as nothing" behavior.
func resolvePath(path string, vars scope) any {
	segments := strings.Split(path, ".")
	cur := vars[segments[0]]
	for _, seg := range segments[1:] {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[seg]
	}
	return cur
}

// eval evaluates a full "{{ }}"-style expression: a base value plus any
// "| filter: args" pipeline.
func eval(raw string, vars scope, errs *[]string) any {
	parts := splitTopLevel(raw, '|')
	val := evalValue(parts[0], vars)
	for _, f := range parts[1:] {
		val = applyFilter(strings.TrimSpace(f), val, vars, errs)
	}
	return val
}

// applyFilter applies one "name" or "name: arg1, arg2" filter segment. An unrecognized
// filter is reported as an error, not silently ignored.
func applyFilter(seg string, val any, vars scope, errs *[]string) any {
	name := seg
	var argsRaw string
	if idx := strings.IndexByte(seg, ':'); idx >= 0 {
		name = strings.TrimSpace(seg[:idx])
		argsRaw = seg[idx+1:]
	}
	args := []any{}
	if argsRaw != "" {
		for _, a := range splitTopLevel(argsRaw, ',') {
			args = append(args, evalValue(a, vars))
		}
	}

	switch name {
	case "default":
		if isBlank(val) && len(args) > 0 {
			return args[0]
		}
		return val
	case "asset_url":
		return "/theme-assets/" + toDisplayString(val)
	case "plus":
		if len(args) == 0 {
			return val
		}
		return toNumber(val) + toNumber(args[0])
	case "size":
		return sizeOf(val)
	case "slice":
		return sliceOf(val, args)
	case "strip":
		return strings.TrimSpace(toDisplayString(val))
	case "upcase":
		return strings.ToUpper(toDisplayString(val))
	default:
		*errs = append(*errs, fmt.Sprintf("unsupported filter: %q", name))
		return val
	}
}

func isBlank(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	}
	return false
}

func toNumber(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}

func sizeOf(v any) int {
	switch x := v.(type) {
	case string:
		return len(x)
	case []any:
		return len(x)
	case map[string]any:
		return len(x)
	}
	return 0
}

func sliceOf(v any, args []any) any {
	s, ok := v.(string)
	if !ok || len(args) == 0 {
		return v
	}
	start := int(toNumber(args[0]))
	length := 1
	if len(args) > 1 {
		length = int(toNumber(args[1]))
	}
	if start < 0 || start >= len(s) {
		return ""
	}
	end := start + length
	// length is arbitrary model-supplied input; a negative value must not push end below
	// start, or s[start:end] panics with "slice bounds out of range".
	if end < start {
		end = start
	}
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}

// toDisplayString renders val the way {{ }} output would: standard scalar formatting, "" for nil.
func toDisplayString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	default:
		return fmt.Sprintf("%v", x)
	}
}

// isTruthy: nil/false/""/0/"0" are falsy, everything else is truthy (the string "false" is
// intentionally not special-cased).
func isTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != "" && x != "0"
	case float64:
		return x != 0
	case int:
		return x != 0
	}
	return true
}

// looseEqual: true/1/"1" are one equivalence class, false/0/"0" another, else compares by string.
func looseEqual(a, b any) bool {
	if boolish, ok := boolishClass(a); ok {
		if bboolish, ok2 := boolishClass(b); ok2 {
			return boolish == bboolish
		}
	}
	return toDisplayString(a) == toDisplayString(b)
}

func boolishClass(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case float64:
		if x == 0 {
			return false, true
		}
		if x == 1 {
			return true, true
		}
	case string:
		if x == "0" {
			return false, true
		}
		if x == "1" || x == "true" {
			return true, true
		}
		if x == "false" {
			return false, true
		}
	}
	return false, false
}

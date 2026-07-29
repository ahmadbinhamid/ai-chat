package themecheck

import (
	"fmt"
	"strings"
)

const ruleIDBoolGuard = "bool-guard"

// boolIshLastSegments are the trailing field names spec §1/§7 mark as
// bool-ish (values arrive inconsistently as true/false/1/0/"1"/"0") — keyed
// on the reference's final dotted segment so both "product.on_sale" and,
// after one-hop loop aliasing, "item.active" (from menu.items[].active) match
// the same way.
var boolIshLastSegments = map[string]bool{
	"active": true, "on_sale": true, "has_choices": true, "has_variants": true, "is_available": true,
}

// checkBoolGuard enforces rule 9: a bare {% if x %} on a bool-ish field must
// instead be {% if x == true or x == 1 %}.
func checkBoolGuard(p Proposal, _ Snapshot) []Finding {
	var findings []Finding
	for _, f := range p.Files {
		if !strings.HasSuffix(f.Path, ".liquid") {
			continue
		}
		for _, cond := range ScanIfConditions(f.Content) {
			if !cond.Bare {
				continue
			}
			for _, ref := range cond.Refs {
				if !isBoolIshRef(ref) {
					continue
				}
				findings = append(findings, boolGuardFinding(f.Path, fmt.Sprintf(
					"line %d: '{%% if %s %%}' is a bare truthy check on a bool-ish field — booleans from the backend "+
						"are inconsistent (true/false/1/0/\"1\"/\"0\"), so guard it as '{%% if %s == true or %s == 1 %%}'.",
					cond.Line, ref, ref, ref)))
			}
		}
	}
	return findings
}

func isBoolIshRef(ref string) bool {
	if ref == "customer_authenticated" {
		return true
	}
	last := ref
	if idx := strings.LastIndexByte(ref, '.'); idx >= 0 {
		last = ref[idx+1:]
	}
	return boolIshLastSegments[last]
}

func boolGuardFinding(path, message string) Finding {
	return Finding{Path: path, Rule: ruleIDBoolGuard, Severity: SeverityError, Message: message}
}

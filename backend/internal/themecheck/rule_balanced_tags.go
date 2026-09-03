package themecheck

import (
	"fmt"
	"strings"
)

const ruleIDBalancedTags = "balanced-tags"

// blockOpeners maps an opening tag name to its required closing tag name.
var blockOpeners = map[string]string{
	"if": "endif", "for": "endfor", "capture": "endcapture", "comment": "endcomment",
}

// blockClosers is the inverse of blockOpeners.
var blockClosers = map[string]string{
	"endif": "if", "endfor": "for", "endcapture": "capture", "endcomment": "comment",
}

// checkBalancedTags enforces rule 3: every if/for/capture/comment has its
// matching close, correctly nested — via a simple open/close stack over the
// tag stream, since this dialect has no other block-scoped tags to confuse
// the matching.
func checkBalancedTags(p Proposal, _ Snapshot) []Finding {
	var findings []Finding
	for _, f := range p.Files {
		if !strings.HasSuffix(f.Path, ".liquid") {
			continue
		}
		findings = append(findings, checkBalancedTagsInFile(f.Path, f.Content)...)
	}
	return findings
}

type openTag struct {
	name string
	line int
}

func checkBalancedTagsInFile(path, content string) []Finding {
	var findings []Finding
	var stack []openTag

	for _, t := range ScanTags(content) {
		switch {
		case blockOpeners[t.Name] != "":
			stack = append(stack, openTag{name: t.Name, line: t.Line})

		case t.Name == "elsif" || t.Name == "else":
			if len(stack) == 0 || stack[len(stack)-1].name != "if" {
				findings = append(findings, balancedTagsFinding(path, t.Line, fmt.Sprintf(
					"'{%% %s %%}' on line %d has no enclosing '{%% if %%}' — it must appear directly inside an if-block.",
					t.Name, t.Line)))
			}

		case blockClosers[t.Name] != "":
			opener := blockClosers[t.Name]
			if len(stack) == 0 {
				findings = append(findings, balancedTagsFinding(path, t.Line, fmt.Sprintf(
					"'{%% %s %%}' on line %d has no matching '{%% %s %%}' to close.", t.Name, t.Line, opener)))
				continue
			}
			top := stack[len(stack)-1]
			if top.name != opener {
				findings = append(findings, balancedTagsFinding(path, t.Line, fmt.Sprintf(
					"'{%% %s %%}' on line %d closes '{%% %s %%}' but the innermost open block is '{%% %s %%}' opened on "+
						"line %d — tags must nest correctly.", t.Name, t.Line, opener, top.name, top.line)))
				continue
			}
			stack = stack[:len(stack)-1]
		}
	}

	for _, open := range stack {
		findings = append(findings, balancedTagsFinding(path, open.line, fmt.Sprintf(
			"'{%% %s %%}' opened on line %d is never closed with '{%% %s %%}'.", open.name, open.line, blockOpeners[open.name])))
	}

	return findings
}

func balancedTagsFinding(path string, line int, message string) Finding {
	return Finding{Path: path, Rule: ruleIDBalancedTags, Severity: SeverityError, Message: message, Line: line}
}

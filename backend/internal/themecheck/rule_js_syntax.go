package themecheck

import (
	"fmt"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

const ruleIDJSSyntax = "js-syntax"

// checkJSSyntax enforces §10's "the file runs at all": every proposed .js file must parse, since a single syntax error
// stops the whole script. Transform, not Build: Build always touches the real filesystem, Transform uses a mock one.
func checkJSSyntax(p Proposal, snap Snapshot) []Finding {
	var findings []Finding
	for _, f := range p.Files {
		if !strings.HasSuffix(f.Path, ".js") {
			continue
		}
		fileFindings := checkJSSyntaxInFile(f.Path, f.Content)
		// Missing-brace errors usually land on a blank EOF line, which line-matching downgrade never matches, so a
		// file that already failed to parse before this proposal is judged here instead: it can't block unrelated edits.
		if baseline, ok := snap.Files[f.Path]; ok && len(fileFindings) > 0 && len(checkJSSyntaxInFile(f.Path, baseline)) > 0 {
			for i := range fileFindings {
				fileFindings[i].Severity = SeverityWarning
			}
		}
		findings = append(findings, fileFindings...)
	}
	return findings
}

// checkJSSyntaxInFile parses with IIFE output because layout-end.liquid loads scripts as classic <script defer>; that
// rejects top-level await, but esbuild still silently converts static import/export, so those aren't caught here.
func checkJSSyntaxInFile(path, content string) []Finding {
	result := api.Transform(content, api.TransformOptions{
		Loader:     api.LoaderJS,
		Format:     api.FormatIIFE,
		Sourcefile: path,
		LogLevel:   api.LogLevelSilent,
	})

	findings := make([]Finding, 0, len(result.Errors))
	for _, msg := range result.Errors {
		line, offset := 0, 0
		if msg.Location != nil {
			line = msg.Location.Line
			offset = lineStartOffset(content, line) + msg.Location.Column
		}
		findings = append(findings, jsSyntaxFinding(path, line, offset, msg.Text))
	}
	return findings
}

// lineStartOffset returns the byte offset where 1-based line n starts, clamped to len(content).
func lineStartOffset(content string, n int) int {
	offset := 0
	for i := 1; i < n; i++ {
		next := strings.IndexByte(content[offset:], '\n')
		if next < 0 {
			return len(content)
		}
		offset += next + 1
	}
	return offset
}

func jsSyntaxFinding(path string, line, offset int, reason string) Finding {
	where := path
	if line > 0 {
		where = fmt.Sprintf("%s line %d", path, line)
	}
	return Finding{
		Path: path, Rule: ruleIDJSSyntax, Severity: SeverityError, Line: line, Offset: offset,
		Message: fmt.Sprintf("%s: %s — the file won't run at all until this is fixed.", where, reason),
	}
}

// Package themecheck enforces theme_engine_spec.md's hard rules against a
// model's proposed file changes before they're allowed to reach disk (see
// no network, no disk — same discipline as themefs's own pure-function files
// (pathsafety.go, layout.go, pages.go).
package themecheck

import (
	"strings"

	"ai-chat/internal/themefs"
)

// Severity is how a Finding should be treated by the caller: an error
// finding blocks the write and triggers a retry; a warning finding is
// surfaced to the merchant but doesn't block.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Finding is one rule violation. Message is written for the model to act on
// — it should say what's wrong and what the correct form is, since a failed
// generation is retried with these findings fed back as the next turn.
type Finding struct {
	Path     string // theme-relative, or "" for a theme-wide finding
	Rule     string // stable id, e.g. "page-boilerplate"
	Severity Severity
	Message  string
	// Line is the 1-based line in the proposed file's content the violation
	// is anchored to, or 0 when a rule has no single meaningful line for it
	// (a theme-wide finding, an existence check, etc.). Populated only where
	// a rule already computes one for its Message (see lineAt and the Tag/
	// Expression.Line fields it backs) — never parsed back out of Message,
	// which stays free to reword. See DowngradePreExistingFindings, the one
	// consumer that needs this as data rather than prose.
	Line int
	// Offset is the 0-based byte offset into that file's proposed content
	// where the violation starts, or 0 when a rule has no meaningful offset
	// for it — same convention as Line, with one difference: Line is
	// 1-based so 0 unambiguously means "unset", but a real match CAN start
	// at byte 0, so Offset's zero value isn't itself proof of "unset". Only
	// rely on it once Rule/Severity are already known to be ones that always
	// populate it. See AutoFixThemeTokens, the one consumer that needs
	// scoping precise to the byte — a flagged declaration sharing a line
	// with a grandfathered one is exactly the ambiguity Line alone can't
	// resolve.
	Offset int
}

// Snapshot is the current theme state a Proposal is checked against.
// Files and Paths are deliberately separate types, not one map with empty-
// string placeholders: Paths says what exists (the whole theme — every file
// path, whether or not this package ever reads its content); Files says what
// content this package actually fetched (pages.json, defaults.json, the two
// layout files). A rule that wants a file's content must go through Files,
// where absence unambiguously means "never fetched" — never "exists but
// empty", which an empty-string placeholder in a single shared map could not
// distinguish.
type Snapshot struct {
	Files map[string]string // theme-relative path -> content, for the handful of files this package reads
	Paths map[string]bool   // every theme-relative file path that exists, content or not — for existence checks only (rule 4)
}

// HasPath reports whether relPath exists anywhere in the theme.
func (s Snapshot) HasPath(relPath string) bool { return s.Paths[relPath] }

const (
	pathPagesJSON    = "pages.json"
	pathDefaultsJSON = "defaults.json"
	pathLayoutStart  = "liquid/layout-start.liquid"
	pathLayoutEnd    = "liquid/layout-end.liquid"
)

// PagesJSON returns the current pages.json content, or "" if none exists yet.
func (s Snapshot) PagesJSON() string { return s.Files[pathPagesJSON] }

// DefaultsJSON returns the current defaults.json content, or "" if none exists yet.
func (s Snapshot) DefaultsJSON() string { return s.Files[pathDefaultsJSON] }

// LayoutStart returns the current liquid/layout-start.liquid content, or ""
// if none exists yet.
func (s Snapshot) LayoutStart() string { return s.Files[pathLayoutStart] }

// LayoutEnd returns the current liquid/layout-end.liquid content, or "" if
// none exists yet.
func (s Snapshot) LayoutEnd() string { return s.Files[pathLayoutEnd] }

// ProposedFile is one file the model proposes creating, updating, or
// deleting — mirrors ai.GeneratedFile's shape without importing package ai.
type ProposedFile struct {
	Path    string
	Action  string // "create" | "update" | "delete"
	Content string
}

// isDeleteAction is true for propose_changes action "delete" (and the
// staged tombstone marker). Content rules must not run on these — empty
// bodies are intentional.
func isDeleteAction(f ProposedFile) bool {
	if strings.EqualFold(strings.TrimSpace(f.Action), "delete") {
		return true
	}
	return f.Content == themefs.DraftDeleteMarker
}

// withoutDeletes returns a proposal copy with delete files removed so
// content/boilerplate rules never reject an intentional file removal.
func withoutDeletes(p Proposal) Proposal {
	out := p
	out.Files = nil
	for _, f := range p.Files {
		if isDeleteAction(f) {
			continue
		}
		out.Files = append(out.Files, f)
	}
	return out
}

// Proposal is the minimal subset of ai.Result Check needs. The caller
// (themebuild) maps ai.Result into this at the call site: PageRegistryEntry
// carries over unchanged since ai.Result already types that field as
// *themefs.PageEntry, so no duplicate struct is needed for it.
type Proposal struct {
	Files              []ProposedFile
	PageRegistryEntry  *themefs.PageEntry
	LayoutLinksToAdd   []string
	LayoutScriptsToAdd []string
}

// fileByPath returns the content and action ("" if not found) of the
// proposal's file at path, if any — used by rules that need to check
// whether a render target or asset the proposal depends on is itself part
// of the same proposal.
func (p Proposal) fileByPath(path string) (ProposedFile, bool) {
	for _, f := range p.Files {
		if f.Path == path {
			return f, true
		}
	}
	return ProposedFile{}, false
}

// Check runs every rule against proposal given the theme's current snap,
// returning every Finding across all rules and files. Rules run
// independently and unconditionally — a failure in one rule never skips
// another. Delete actions are excluded from content rules (empty body is
// intentional — Apply removes the file).
func Check(proposal Proposal, snap Snapshot) []Finding {
	content := withoutDeletes(proposal)
	var findings []Finding
	findings = append(findings, checkPageBoilerplate(content, snap)...)
	findings = append(findings, checkPlaceholderBody(content, snap)...)
	findings = append(findings, checkAllowedSyntax(content, snap)...)
	findings = append(findings, checkBalancedTags(content, snap)...)
	findings = append(findings, checkRenderTargetExists(content, snap)...)
	findings = append(findings, checkAssetRegistered(content, snap)...)
	findings = append(findings, checkPageRoute(content, snap)...)
	findings = append(findings, checkPageRequiresAuth(content, snap)...)
	findings = append(findings, checkSEOFilled(content, snap)...)
	findings = append(findings, checkThemeToken(content, snap)...)
	findings = append(findings, checkBoolGuard(content, snap)...)
	findings = append(findings, checkAppendNullSafe(content, snap)...)
	findings = append(findings, checkNoFramework(content, snap)...)
	findings = append(findings, checkJSShape(content, snap)...)
	findings = append(findings, checkKnownFields(content, snap)...)
	return findings
}

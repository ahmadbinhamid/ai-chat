// Package themecheck enforces theme_engine_spec.md's hard rules against a proposal before it reaches disk — no network, no disk, pure functions only.
package themecheck

import "ai-chat/internal/themefs"

// Severity is how a Finding should be treated: error blocks the write and retries, warning is surfaced but doesn't block.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Finding is one rule violation; Message is written for the model to act on since findings are fed back as the next turn's prompt.
type Finding struct {
	Path     string // theme-relative, or "" for a theme-wide finding
	Rule     string // stable id, e.g. "page-boilerplate"
	Severity Severity
	Message  string
	// Line is the 1-based line the violation is anchored to, or 0 if none applies. Never parsed back out of Message —
	// DowngradePreExistingFindings is the one consumer that needs it as data.
	Line int
	// Offset is the 0-based byte offset where the violation starts. Unlike Line, 0 is a valid real value here (not just
	// "unset") — only rely on it for Rule/Severity combos known to always populate it (see AutoFixThemeTokens).
	Offset int
}

// Snapshot is the theme state a Proposal is checked against. Files and Paths are deliberately separate: Paths says
// what exists theme-wide; Files says what content was actually fetched, where absence means "never fetched," not "empty".
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

// ProposedFile is one file the model proposes creating or updating; mirrors ai.GeneratedFile's shape without importing package ai.
type ProposedFile struct {
	Path    string
	Action  string // "create" | "update"
	Content string
}

// Proposal is the minimal subset of ai.Result Check needs; themebuild maps ai.Result into this at the call site.
type Proposal struct {
	Files              []ProposedFile
	PageRegistryEntry  *themefs.PageEntry
	LayoutLinksToAdd   []string
	LayoutScriptsToAdd []string
}

// fileByPath returns the proposal's file at path, if any — used by rules checking whether a dependency is part of this same proposal.
func (p Proposal) fileByPath(path string) (ProposedFile, bool) {
	for _, f := range p.Files {
		if f.Path == path {
			return f, true
		}
	}
	return ProposedFile{}, false
}

// Check runs every rule against proposal and snap, returning every Finding; rules run independently — one failing never skips another.
func Check(proposal Proposal, snap Snapshot) []Finding {
	var findings []Finding
	findings = append(findings, checkPageBoilerplate(proposal, snap)...)
	findings = append(findings, checkPlaceholderBody(proposal, snap)...)
	findings = append(findings, checkAllowedSyntax(proposal, snap)...)
	findings = append(findings, checkBalancedTags(proposal, snap)...)
	findings = append(findings, checkRenderTargetExists(proposal, snap)...)
	findings = append(findings, checkAssetRegistered(proposal, snap)...)
	findings = append(findings, checkPageRoute(proposal, snap)...)
	findings = append(findings, checkPageRequiresAuth(proposal, snap)...)
	findings = append(findings, checkSEOFilled(proposal, snap)...)
	findings = append(findings, checkThemeToken(proposal, snap)...)
	findings = append(findings, checkBoolGuard(proposal, snap)...)
	findings = append(findings, checkNoFramework(proposal, snap)...)
	findings = append(findings, checkJSShape(proposal, snap)...)
	findings = append(findings, checkKnownFields(proposal, snap)...)
	return findings
}

package builderoperations

import (
	"context"
	"regexp"
	"strings"
	"time"

	"ai-chat/internal/builderplan"
	"ai-chat/internal/themefs"
)

// Diagnosis is a structured structural check of one existing page.
// No raw prompts — only boolean flags and short issue codes.
type Diagnosis struct {
	Slug                 string   `json:"slug,omitempty"`
	Path                 string   `json:"path,omitempty"`
	PageExists           bool     `json:"page_exists"`
	Registered           bool     `json:"registered"`
	RouteValid           bool     `json:"route_valid"`
	TemplateValid        bool     `json:"template_valid"`
	DependenciesValid    bool     `json:"dependencies_valid"`
	NavigationRegistered bool     `json:"navigation_registered"`
	Issues               []string `json:"issues,omitempty"`
	FixApplied           bool     `json:"fix_applied"`
}

// DiagnoseExistingPage inspects an existing page (file + registry + basic
// template/deps) and may apply a safe registry fix when the liquid exists
// but is unregistered. Never calls DeepSeek / local LM.
type DiagnoseExistingPage struct{}

func (DiagnoseExistingPage) Name() string { return NameDiagnoseExistingPage }

func (op DiagnoseExistingPage) Execute(ctx context.Context, in Input) (Result, error) {
	start := time.Now()
	base := Result{
		Metrics: Metrics{
			Called:        true,
			Name:          NameDiagnoseExistingPage,
			DeepSeekCalls: 0,
		},
	}
	finish := func(r Result) Result {
		r.Metrics.ElapsedMs = time.Since(start).Milliseconds()
		r.Metrics.Called = true
		r.Metrics.Name = NameDiagnoseExistingPage
		r.Metrics.DeepSeekCalls = 0
		switch r.Outcome {
		case OutcomeSuccess, OutcomeAlreadyDone, OutcomeNoChange:
			r.Metrics.Success = true
			if r.Outcome == OutcomeAlreadyDone || r.Outcome == OutcomeNoChange {
				r.Metrics.AlreadyDone = true
			}
		case OutcomeNotFound:
			r.Metrics.NotFound = true
		case OutcomeAmbiguous:
			r.Metrics.Ambiguous = true
		}
		return r
	}

	if in.Store == nil {
		base.Outcome = OutcomeFailed
		base.UserMessage = "I couldn't check that page right now."
		return finish(base), nil
	}

	eligible := MatchesTroubleshootPrompt(in.Prompt)
	if in.Plan != nil {
		if n, ok := operationNameFromPlan(*in.Plan); ok && n == NameDiagnoseExistingPage {
			eligible = true
		}
		if in.Plan.Intent == builderplan.IntentPageTroubleshoot {
			eligible = true
		}
	}
	if !eligible {
		base.Outcome = OutcomeNotApplicable
		base.Metrics.Called = false
		return finish(base), nil
	}

	slug := targetSlugFromPrompt(in.Prompt)
	if slug == "" && in.Plan != nil {
		slug = slugFromPlan(*in.Plan)
	}
	if slug == "" && in.Plan != nil {
		for _, t := range in.Plan.Targets {
			if s := targetSlugFromPlan(t); s != "" {
				slug = s
				break
			}
		}
	}
	if slug == "" {
		base.Outcome = OutcomeAmbiguous
		base.NeedsClarification = true
		base.UserMessage = "Which page should I check — blog, pricing, about, or another page?"
		return finish(base), nil
	}

	diag := Diagnosis{Slug: slug, Path: "pages/" + slug + ".liquid"}
	tree, err := in.Store.ListFiles(ctx, in.Auth)
	if err != nil {
		base.Outcome = OutcomeFailed
		base.UserMessage = "I couldn't check that page right now."
		return finish(base), nil
	}
	onDisk := map[string]string{}
	for _, p := range flattenThemePaths(tree) {
		onDisk[strings.ToLower(p)] = p
	}
	want := "pages/" + slug + ".liquid"
	actual, exists := onDisk[want]
	diag.PageExists = exists
	if exists {
		diag.Path = actual
	}

	pagesRaw, err := in.Store.ReadFile(ctx, in.Auth, pathPagesJSON)
	if err != nil {
		base.Outcome = OutcomeFailed
		base.UserMessage = "I couldn't check that page right now."
		return finish(base), nil
	}
	rows, parseErr := parsePagesJSONRows(pagesRaw)
	if parseErr != nil {
		diag.Issues = append(diag.Issues, "registry_invalid")
		base.Outcome = OutcomeFailed
		base.UserMessage = "I checked the " + friendlyPageName(slug) + " page and found a problem with its registration data. Please try again."
		base.Diagnosis = &diag
		return finish(base), nil
	}
	registeredPaths := registeredLiquidPaths(rows)
	registeredIDs := map[string]bool{}
	var routeOK bool
	for _, r := range rows {
		id := strings.ToLower(pageRegistryIdentity(r.Page, r.Slug))
		if id != "" {
			registeredIDs[id] = true
		}
		if id == slug {
			routeOK = strings.EqualFold(strings.TrimSpace(r.Slug), slug) &&
				(strings.TrimSpace(r.Page) == "" || strings.EqualFold(strings.TrimSpace(r.Page), slug))
		}
	}
	diag.Registered = registeredPaths[strings.ToLower(diag.Path)] || registeredIDs[slug]
	diag.RouteValid = diag.Registered && routeOK
	if diag.Registered && !routeOK {
		diag.Issues = append(diag.Issues, "route_mismatch")
	}

	if !diag.PageExists {
		diag.Issues = append(diag.Issues, "file_missing")
		base.Outcome = OutcomeNotFound
		base.UserMessage = "I checked for the " + friendlyPageName(slug) + " page and couldn't find its file. Tell me if you meant a different page."
		base.Diagnosis = &diag
		return finish(base), nil
	}

	content, err := in.Store.ReadFile(ctx, in.Auth, diag.Path)
	if err != nil || strings.TrimSpace(content) == "" {
		diag.TemplateValid = false
		diag.Issues = append(diag.Issues, "template_empty")
	} else {
		diag.TemplateValid = true
		if !strings.Contains(content, "layout-start") || !strings.Contains(content, "layout-end") {
			diag.Issues = append(diag.Issues, "missing_layout_markers")
			diag.TemplateValid = false
		}
		// Bounded dependency check: referenced components/css that look missing.
		missing := missingRenderTargets(content, onDisk)
		if len(missing) > 0 {
			diag.DependenciesValid = false
			diag.Issues = append(diag.Issues, "missing_dependencies")
		} else {
			diag.DependenciesValid = true
		}
	}

	defaultsRaw, _ := in.Store.ReadFile(ctx, in.Auth, "defaults.json")
	diag.NavigationRegistered = menuMentionsSlug(defaultsRaw, slug)

	// Safe auto-fix: file exists but not registered → register_existing path.
	if diag.PageExists && !diag.Registered {
		diag.Issues = append(diag.Issues, "not_registered")
		entry := normalizeRegistryEntry(&themefs.PageEntry{
			Title:  titleFromSlug(slug),
			Slug:   slug,
			Page:   slug,
			Path:   "/pages",
			Type:   registryTypeForSlug(slug),
			Status: "published",
		})
		merged, added, mergeErr := mergePageRegistryEntries(pagesRaw, []*themefs.PageEntry{entry})
		if mergeErr == nil && validatePagesJSONNonDestructive(pagesRaw, merged, added, true) == nil &&
			validateRegisterStaging(pagesRaw, merged, diag.Path, slug, entry) == nil {
			diag.FixApplied = true
			diag.Registered = true
			diag.RouteValid = true
			base.Outcome = OutcomeSuccess
			base.UserMessage = "I found the issue and fixed the " + friendlyPageName(slug) + " page — it wasn't registered in the site routes."
			base.PageEntry = entry
			base.Files = []FileChange{
				{Path: diag.Path, Action: "update", Content: content},
				{Path: pathPagesJSON, Action: "update", Content: merged},
			}
			base.Diagnosis = &diag
			return finish(base), nil
		}
		base.Outcome = OutcomeFailed
		base.UserMessage = "I checked the " + friendlyPageName(slug) + " page and found an issue with its route/registration, but couldn't apply a safe fix automatically."
		base.Diagnosis = &diag
		return finish(base), nil
	}

	if len(diag.Issues) == 0 {
		base.Outcome = OutcomeNoChange
		base.UserMessage = "I checked the " + friendlyPageName(slug) + " page and its registration and didn't find a structural issue. Tell me what happens when you open it."
		base.Diagnosis = &diag
		return finish(base), nil
	}

	// Non-registry issues: diagnose only (no silent unrelated rewrite).
	base.Outcome = OutcomeNoChange
	base.UserMessage = diagnoseUserMessage(slug, diag)
	base.Diagnosis = &diag
	return finish(base), nil
}

func diagnoseUserMessage(slug string, d Diagnosis) string {
	name := friendlyPageName(slug)
	if !d.TemplateValid {
		return "I found a problem with the " + name + " page template and I'm checking the affected files."
	}
	if !d.DependenciesValid {
		return "I found a problem with the " + name + " page — some of its referenced pieces are missing."
	}
	if !d.RouteValid {
		return "I checked the " + name + " page and found an issue with its route/registration."
	}
	return "I checked the " + name + " page and found an issue that needs a closer look."
}

func friendlyPageName(slug string) string {
	switch slug {
	case "blog":
		return "blog"
	case "home":
		return "home"
	default:
		return strings.ReplaceAll(slug, "-", " ")
	}
}

func missingRenderTargets(content string, onDisk map[string]string) []string {
	// Very small scan: {% render 'x' %} / {% include 'x' %} → components/x.liquid
	var missing []string
	low := strings.ToLower(content)
	for _, marker := range []string{"{% render '", "{% render \"", "{% include '", "{% include \""} {
		idx := 0
		for {
			i := strings.Index(low[idx:], marker)
			if i < 0 {
				break
			}
			i += idx + len(marker)
			end := strings.IndexAny(low[i:], "'\"")
			if end <= 0 {
				break
			}
			name := strings.TrimSpace(low[i : i+end])
			idx = i + end
			if name == "" || strings.Contains(name, "/") {
				continue
			}
			want := "components/" + name + ".liquid"
			if _, ok := onDisk[want]; !ok {
				missing = append(missing, want)
			}
			if len(missing) >= 5 {
				return missing
			}
		}
	}
	return missing
}

func menuMentionsSlug(defaultsJSON, slug string) bool {
	if strings.TrimSpace(defaultsJSON) == "" || slug == "" {
		return false
	}
	low := strings.ToLower(defaultsJSON)
	return strings.Contains(low, "/"+slug) || strings.Contains(low, "\""+slug+"\"") ||
		strings.Contains(low, "pages/"+slug)
}

// MatchesTroubleshootPrompt reports broken/not-opening/check-and-fix language.
func MatchesTroubleshootPrompt(prompt string) bool {
	p := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
	if p == "" {
		return false
	}
	return pageTroubleshootPromptRe.MatchString(p)
}

var pageTroubleshootPromptRe = regexp.MustCompile(`(?i)(?:` +
	`\b(?:page|blog|pricing|about|contact|faq|it|this)\b[\s\S]{0,48}\b(?:not\s+(?:working|opening|loading|open)|won'?t\s+open|doesn'?t\s+open|is\s+broken|broken)\b` +
	`|` +
	`\b(?:not\s+(?:working|opening|loading)|won'?t\s+open|doesn'?t\s+open|is\s+broken)\b` +
	`|` +
	`\b(?:check\s+and\s+fix|troubleshoot|debug)\b` +
	`|` +
	`\bwhy\b[\s\S]{0,48}\b(?:not\s+(?:working|opening|open)|broken)\b` +
	`|` +
	`\b(?:please\s+)?check\b[\s\S]{0,32}\b(?:the\s+)?(?:\w+\s+)?page\b` +
	`|` +
	`\bfix\b[\s\S]{0,32}\b(?:the\s+)?(?:\w+\s+)?page\b` +
	`)`)

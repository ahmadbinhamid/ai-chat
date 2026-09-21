package builderoperations

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ai-chat/internal/builderplan"
	"ai-chat/internal/themefs"
)

// RegisterExistingPage registers an on-disk page liquid that is missing from
// pages.json. Never regenerates content, never calls DeepSeek / local ML.
type RegisterExistingPage struct{}

func (RegisterExistingPage) Name() string { return NameRegisterExistingPage }

func (op RegisterExistingPage) Execute(ctx context.Context, in Input) (Result, error) {
	start := time.Now()
	base := Result{
		Metrics: Metrics{
			Called:        true,
			Name:          NameRegisterExistingPage,
			DeepSeekCalls: 0,
		},
	}
	finish := func(r Result) Result {
		r.Metrics.ElapsedMs = time.Since(start).Milliseconds()
		r.Metrics.Called = true
		r.Metrics.Name = NameRegisterExistingPage
		r.Metrics.DeepSeekCalls = 0
		switch r.Outcome {
		case OutcomeSuccess:
			r.Metrics.Success = true
		case OutcomeAlreadyDone, OutcomeNoChange:
			r.Metrics.AlreadyDone = true
			r.Metrics.Success = true
		case OutcomeNotFound:
			r.Metrics.NotFound = true
		case OutcomeAmbiguous:
			r.Metrics.Ambiguous = true
		}
		return r
	}

	if in.Store == nil {
		base.Outcome = OutcomeFailed
		base.UserMessage = msgGenericFailure
		return finish(base), nil
	}

	eligible := MatchesRegisterExistingPrompt(in.Prompt)
	if in.Plan != nil && IsDeterministicPlan(*in.Plan) {
		if n, ok := operationNameFromPlan(*in.Plan); ok && n == NameRegisterExistingPage {
			eligible = true
		}
	}
	if !eligible {
		base.Outcome = OutcomeNotApplicable
		base.Metrics.Called = false
		return finish(base), nil
	}

	wantSlug := targetSlugFromPrompt(in.Prompt)
	if wantSlug == "" && in.Plan != nil {
		wantSlug = slugFromPlan(*in.Plan)
	}

	tree, err := in.Store.ListFiles(ctx, in.Auth)
	if err != nil {
		base.Outcome = OutcomeFailed
		base.UserMessage = msgGenericFailure
		return finish(base), nil
	}
	allPaths := flattenThemePaths(tree)
	onDisk := map[string]string{}
	for _, p := range allPaths {
		onDisk[strings.ToLower(p)] = p
	}

	pagesRaw, err := in.Store.ReadFile(ctx, in.Auth, pathPagesJSON)
	if err != nil {
		base.Outcome = OutcomeFailed
		base.UserMessage = msgGenericFailure
		return finish(base), nil
	}
	rows, err := parsePagesJSONRows(pagesRaw)
	if err != nil {
		base.Outcome = OutcomeFailed
		base.UserMessage = msgGenericFailure
		return finish(base), nil
	}
	registered := registeredLiquidPaths(rows)
	registeredIDs := map[string]bool{}
	for _, r := range rows {
		id := pageRegistryIdentity(r.Page, r.Slug)
		if id != "" {
			registeredIDs[strings.ToLower(id)] = true
		}
	}

	candidates := unregisteredPageLiquids(allPaths, registered)
	sort.Strings(candidates)

	var targetPath string
	if wantSlug != "" {
		want := "pages/" + wantSlug + ".liquid"
		actual, exists := onDisk[want]
		if !exists {
			base.Outcome = OutcomeNotFound
			base.UserMessage = msgNotFound
			return finish(base), nil
		}
		targetPath = actual
		if registered[strings.ToLower(targetPath)] || registeredIDs[wantSlug] {
			base.Outcome = OutcomeAlreadyDone
			base.UserMessage = msgAlreadyRegistered(wantSlug)
			return finish(base), nil
		}
	} else {
		switch len(candidates) {
		case 0:
			base.Outcome = OutcomeAlreadyDone
			base.UserMessage = msgAllAlreadyRegistered
			return finish(base), nil
		case 1:
			targetPath = candidates[0]
		default:
			base.Outcome = OutcomeAmbiguous
			base.NeedsClarification = true
			base.UserMessage = msgAmbiguous("")
			return finish(base), nil
		}
	}

	slug := pageIDFromLiquidPath(targetPath)
	if slug == "" {
		base.Outcome = OutcomeFailed
		base.UserMessage = msgGenericFailure
		return finish(base), nil
	}
	if registered[strings.ToLower(targetPath)] || registeredIDs[slug] {
		base.Outcome = OutcomeAlreadyDone
		base.UserMessage = msgAlreadyRegistered(slug)
		return finish(base), nil
	}

	content, err := in.Store.ReadFile(ctx, in.Auth, targetPath)
	if err != nil {
		base.Outcome = OutcomeFailed
		base.UserMessage = msgGenericFailure
		return finish(base), nil
	}
	if strings.TrimSpace(content) == "" {
		base.Outcome = OutcomeFailed
		base.UserMessage = msgGenericFailure
		return finish(base), nil
	}

	entry := normalizeRegistryEntry(&themefs.PageEntry{
		Title:  titleFromSlug(slug),
		Slug:   slug,
		Page:   slug,
		Path:   "/pages",
		Type:   registryTypeForSlug(slug),
		Status: "published",
	})

	merged, added, err := mergePageRegistryEntries(pagesRaw, []*themefs.PageEntry{entry})
	if err != nil {
		base.Outcome = OutcomeFailed
		base.UserMessage = msgGenericFailure
		return finish(base), nil
	}
	if err := validatePagesJSONNonDestructive(pagesRaw, merged, added, true); err != nil {
		base.Outcome = OutcomeFailed
		base.UserMessage = msgGenericFailure
		return finish(base), nil
	}
	if err := validateRegisterStaging(pagesRaw, merged, targetPath, slug, entry); err != nil {
		base.Outcome = OutcomeFailed
		base.UserMessage = msgGenericFailure
		return finish(base), nil
	}

	base.Outcome = OutcomeSuccess
	base.UserMessage = msgRegistered(slug)
	base.PageEntry = entry
	base.Files = []FileChange{
		{Path: targetPath, Action: "update", Content: content},
		{Path: pathPagesJSON, Action: "update", Content: merged},
	}
	return finish(base), nil
}

func slugFromPlan(plan builderplan.BuilderPlan) string {
	for _, op := range plan.Operations {
		if op.Kind == builderplan.OpRegisterExistingPage || op.Kind == builderplan.OpRegisterPage {
			if s := targetSlugFromPlan(op.Target); s != "" && s != "*" {
				return s
			}
		}
	}
	for _, t := range plan.Targets {
		if s := targetSlugFromPlan(t); s != "" {
			return s
		}
	}
	return ""
}

// validateRegisterStaging checks file ↔ registry consistency for the one
// intended registration before any write.
func validateRegisterStaging(before, after, liquidPath, slug string, entry *themefs.PageEntry) error {
	if entry == nil {
		return fmt.Errorf("missing registry entry")
	}
	id := pageEntryIdentity(normalizeRegistryEntry(entry))
	if id == "" || !strings.EqualFold(id, slug) {
		return fmt.Errorf("canonical identity mismatch")
	}
	if pageIDFromLiquidPath(liquidPath) != slug {
		return fmt.Errorf("liquid path identity mismatch")
	}
	if err := validatePagesJSONNonDestructive(before, after, []string{id}, true); err != nil {
		return err
	}
	afterRaws, err := parsePagesJSONRaw(after)
	if err != nil {
		return err
	}
	found := false
	for _, raw := range afterRaws {
		if strings.EqualFold(identityFromRawPage(raw), id) {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("merged registry missing new identity")
	}
	return nil
}

const (
	msgGenericFailure       = "I couldn't complete that registration."
	msgNotFound             = "I couldn't find that page in the theme."
	msgAllAlreadyRegistered = "All pages are already registered."
)

func msgRegistered(slug string) string {
	return fmt.Sprintf("The %s has been registered successfully.", displayPageLabel(slug))
}

func msgAlreadyRegistered(slug string) string {
	return fmt.Sprintf("The %s is already registered.", displayPageLabel(slug))
}

func msgAmbiguous(hintSlug string) string {
	if strings.EqualFold(hintSlug, "blog") || hintSlug == "" {
		// Default clarification matches the common blog register ask.
		if hintSlug == "" {
			return "Which page would you like me to register?"
		}
		return "Which blog page would you like me to register?"
	}
	return fmt.Sprintf("Which %s would you like me to register?", displayPageLabel(hintSlug))
}

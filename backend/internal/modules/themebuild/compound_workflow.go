package themebuild

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ai-chat/internal/ai"
	"ai-chat/internal/themefs"
)

// Compound step kinds for multi-action builder requests.
const (
	CompoundStepCreatePage    = "create_page"
	CompoundStepRegisterPages = "register_pages"
	CompoundStepAddToMenu     = "add_to_menu"
)

// CompoundStep is one atomic unit of a compound generation workflow.
type CompoundStep struct {
	ID              int
	Kind            string
	Index           int // 1-based page index for create_page
	Of              int // total create_page steps
	Label           string
	FocusedPrompt   string // sent to the model (prepared wrapper applied later)
	ValidatorPrompt string // used by completeness gates (merchant-scoped)
}

// CompoundPlan is the explicit step list for a compound request.
type CompoundPlan struct {
	OriginalPrompt string
	Steps          []CompoundStep
}

// CompoundProgress tracks which steps finished so a later failure can
// preserve earlier work and report an actionable partial message.
type CompoundProgress struct {
	Completed  []CompoundStep
	Failed     *CompoundStep
	Accum      *ai.Result
	Registries []*themefs.PageEntry // page_registry_entry from each create step
	// RegistryJSON is the last validated pages.json checkpoint after
	// structured merges. Never holds a truncated ThemeContext prompt stub.
	RegistryJSON string
	// MenuJSON is the last validated defaults.json checkpoint after
	// structured add_to_menu merges. Never holds a truncated prompt stub
	// or a model-authored full defaults.json rewrite.
	MenuJSON string
}

// PlanCompoundWorkflow returns a multi-step plan for compound builder
// requests (multi-page create, page+menu). Returns ok=false when the
// prompt should stay on the existing single-shot complex_page path.
func PlanCompoundWorkflow(prompt string) (CompoundPlan, bool) {
	p := strings.TrimSpace(prompt)
	if p == "" {
		return CompoundPlan{}, false
	}
	plan := CompoundPlan{OriginalPrompt: p}

	if isMultiPageCreatePrompt(p) {
		n := multiPageCreateBatchSize(p)
		if n < 2 {
			return CompoundPlan{}, false
		}
		topic := compoundTopicHint(p)
		for i := 1; i <= n; i++ {
			plan.Steps = append(plan.Steps, CompoundStep{
				ID:    len(plan.Steps) + 1,
				Kind:  CompoundStepCreatePage,
				Index: i,
				Of:    n,
				Label: fmt.Sprintf("Create page %d of %d", i, n),
				FocusedPrompt: fmt.Sprintf(
					"Atomic step %d of %d for the merchant request %q.\n"+
						"Create EXACTLY ONE new blog/content page (page %d of %d) about: %s.\n"+
						"In ONE propose_changes:\n"+
						"1) action \"create\" on pages/<unique-kebab-slug>.liquid with layout-start/end + real on-topic copy (≈150–250 words).\n"+
						"2) Register it with page_registry_entry ONLY (single structured field — the platform merges it into pages.json).\n"+
						"FORBIDDEN: updating or rewriting pages.json (the platform merges registration for you).\n"+
						"FORBIDDEN: updating pages/blog.liquid, pages/css/blog.css, pages/home.liquid, or any other existing large page.\n"+
						"FORBIDDEN: creating more than one new liquid file in this step.\n"+
						"FORBIDDEN: rewriting the whole theme.",
					i, n, p, i, n, topic,
				),
				ValidatorPrompt: "create 1 blog page and add in pages",
			})
		}
	}

	// Page + navigation compound (even for single-page create with menu).
	if isAddToMenuPrompt(p) && (isMultiPageCreatePrompt(p) || pageCreateRe.MatchString(strings.ToLower(p))) {
		label := menuLabelFromAddPrompt(p)
		hint := "the new page"
		if label != "" {
			hint = label
		}
		plan.Steps = append(plan.Steps, CompoundStep{
			ID:    len(plan.Steps) + 1,
			Kind:  CompoundStepAddToMenu,
			Label: "Add pages to navigation",
			// Deterministic store-backed merge — DeepSeek is not called for this
			// step. FocusedPrompt is retained for plan/debug visibility only.
			FocusedPrompt: fmt.Sprintf(
				"Atomic menu step for merchant request %q.\n"+
					"Platform adds structured menu items for %s into defaults.json menu.items (no model rewrite).",
				p, hint,
			),
			ValidatorPrompt: p,
		})
	}

	if len(plan.Steps) < 2 {
		return CompoundPlan{}, false
	}
	return plan, true
}

func compoundTopicHint(prompt string) string {
	low := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
	switch {
	case strings.Contains(low, "software"):
		return "a software company (CRM/POS/SaaS style topics)"
	case strings.Contains(low, "blog"):
		return "blog posts matching the merchant's words"
	default:
		return "the merchant's stated topic"
	}
}

// multiPageIndexRewritePaths must never be full-rewritten during a multi-page
// create — that is the production failure mode (blog.liquid −1800 lines).
var multiPageIndexRewritePaths = map[string]bool{
	"pages/blog.liquid":                 true,
	"pages/css/blog.css":                true,
	"pages/home.liquid":                 true,
	"pages/css/home.css":                true,
	"components/card-essentials.liquid": true,
}

// RejectOversizedMultiPageIndexRewrite rejects proposals that rewrite the
// blog/home index (or card-essentials) during a multi-page create. Those
// updates are never required to create new pages and cause repair churn.
func RejectOversizedMultiPageIndexRewrite(prompt string, result *ai.Result) error {
	if result == nil || !isMultiPageCreatePrompt(prompt) {
		return nil
	}
	for _, f := range result.Files {
		low := strings.ToLower(strings.TrimSpace(f.Path))
		act := strings.ToLower(strings.TrimSpace(f.Action))
		if !multiPageIndexRewritePaths[low] {
			continue
		}
		if act != "update" && act != "edit" && act != "create" {
			continue
		}
		// Any non-trivial rewrite of the index is rejected — creates of
		// blog.liquid itself are also wrong for "new pages".
		if len(strings.TrimSpace(f.Content)) > 200 || act == "edit" || act == "update" || act == "create" {
			return fmt.Errorf("oversized or forbidden rewrite of %s during multi-page create — create new pages/<slug>.liquid files only", low)
		}
	}
	return nil
}

// StripMultiPageIndexRewrites removes forbidden index rewrites so a mostly
// correct proposal (new liquids + pages.json) can proceed without dragging
// a −1800-line blog.liquid update into checkAndRepair.
func StripMultiPageIndexRewrites(result *ai.Result) *ai.Result {
	if result == nil {
		return nil
	}
	out := cloneResultFiles(result)
	kept := make([]ai.GeneratedFile, 0, len(out.Files))
	for _, f := range out.Files {
		low := strings.ToLower(strings.TrimSpace(f.Path))
		if multiPageIndexRewritePaths[low] {
			continue
		}
		kept = append(kept, f)
	}
	out.Files = kept
	return out
}

// incompleteAtomicPageCreateProposal requires exactly one new page liquid
// create plus registration via page_registry_entry (preferred) or pages.json.
func incompleteAtomicPageCreateProposal(result *ai.Result) error {
	if result == nil {
		return fmt.Errorf("empty proposal")
	}
	if result.NeedsClarification || result.AnsweredQuestion {
		return nil
	}
	pageCreates := 0
	hasPagesJSON := false
	for _, f := range result.Files {
		low := strings.ToLower(strings.TrimSpace(f.Path))
		act := strings.ToLower(strings.TrimSpace(f.Action))
		if low == "pages.json" && (act == "update" || act == "create") {
			hasPagesJSON = true
			continue
		}
		if strings.HasPrefix(low, "pages/") && strings.HasSuffix(low, ".liquid") &&
			!strings.HasPrefix(low, "pages/css/") && !strings.HasPrefix(low, "pages/auth/") &&
			low != "pages/blog.liquid" && low != "pages/home.liquid" {
			if act == "create" {
				pageCreates++
			}
		}
	}
	hasRegistry := result.PageRegistryEntry != nil
	var missing []string
	if pageCreates != 1 {
		missing = append(missing, fmt.Sprintf("exactly 1 new pages/<slug>.liquid create (got %d)", pageCreates))
	}
	if !hasPagesJSON && !hasRegistry {
		missing = append(missing, "page_registry_entry (preferred) or pages.json update registering the new page")
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("incomplete atomic page create: %s", strings.Join(missing, "; "))
}

// MergeCompoundResults folds a successful step into the accumulated draft.
// Later pages.json / defaults.json updates replace earlier ones; other
// paths are appended (first write wins for duplicate non-registry paths).
// PageRegistryEntry from the latest step is kept on the result for
// themecheck; earlier registry entries must be tracked via CompoundProgress.Registries.
func MergeCompoundResults(acc, step *ai.Result) *ai.Result {
	if step == nil {
		return acc
	}
	if acc == nil || len(acc.Files) == 0 {
		return cloneResultFiles(step)
	}
	out := cloneResultFiles(acc)
	byPath := make(map[string]int, len(out.Files))
	for i, f := range out.Files {
		byPath[strings.ToLower(strings.TrimSpace(f.Path))] = i
	}
	for _, f := range step.Files {
		low := strings.ToLower(strings.TrimSpace(f.Path))
		if idx, ok := byPath[low]; ok {
			out.Files[idx] = f
			continue
		}
		byPath[low] = len(out.Files)
		out.Files = append(out.Files, f)
	}
	// Latest registry wins on the Result field (single-value schema); callers
	// accumulate prior entries separately for buildWritePlan(extraEntries).
	if step.PageRegistryEntry != nil {
		entry := *step.PageRegistryEntry
		out.PageRegistryEntry = &entry
	}
	if step.Summary != "" {
		if out.Summary == "" {
			out.Summary = step.Summary
		} else {
			out.Summary = strings.TrimSpace(out.Summary + " " + step.Summary)
		}
	}
	out.InputTokens += step.InputTokens
	out.OutputTokens += step.OutputTokens
	return out
}

// clonePageEntry returns a shallow copy of a page registry entry.
func clonePageEntry(entry *themefs.PageEntry) *themefs.PageEntry {
	if entry == nil {
		return nil
	}
	cp := *entry
	return &cp
}

// accumulateCompoundRegistry records a step's page_registry_entry for later
// staging via buildWritePlan(extraEntries...). Dedupes by liquid path.
func accumulateCompoundRegistry(regs []*themefs.PageEntry, entry *themefs.PageEntry) []*themefs.PageEntry {
	if entry == nil {
		return regs
	}
	want := pageRegistryWantPath(entry)
	for i, existing := range regs {
		if existing != nil && pageRegistryWantPath(existing) == want {
			regs[i] = clonePageEntry(entry)
			return regs
		}
	}
	return append(regs, clonePageEntry(entry))
}

// compoundExtraRegistryEntries returns registries that are not already on
// result.PageRegistryEntry (avoid double-attach in buildWritePlan).
func compoundExtraRegistryEntries(result *ai.Result, regs []*themefs.PageEntry) []*themefs.PageEntry {
	if len(regs) == 0 {
		return nil
	}
	primaryPath := ""
	if result != nil && result.PageRegistryEntry != nil {
		primaryPath = pageRegistryWantPath(result.PageRegistryEntry)
	}
	out := make([]*themefs.PageEntry, 0, len(regs))
	for _, e := range regs {
		if e == nil {
			continue
		}
		if primaryPath != "" && pageRegistryWantPath(e) == primaryPath {
			continue
		}
		out = append(out, e)
	}
	return out
}

// upsertPagesJSONWithRegistry merges one page_registry_entry into a validated
// pages.json checkpoint via structured merge + canonical serialize. On parse
// or merge failure it returns the previous checkpoint unchanged (never wipes
// the registry or concatenates model JSON text).
func upsertPagesJSONWithRegistry(pagesJSON string, entry *themefs.PageEntry) string {
	if entry == nil {
		return pagesJSON
	}
	next, _, err := applyRegistryEntryCheckpoint(pagesJSON, entry)
	if err != nil {
		return pagesJSON
	}
	return next
}

// pageIdentitiesFromJSON lists page/slug identities for compound prompts
// without embedding a truncated pages.json body the model might rewrite.
func pageIdentitiesFromJSON(pagesJSON string) []string {
	raws, err := parsePagesJSONRaw(pagesJSON)
	if err != nil || len(raws) == 0 {
		return nil
	}
	out := make([]string, 0, len(raws))
	for _, raw := range raws {
		if id := identityFromRawPage(raw); id != "" {
			out = append(out, id)
		}
	}
	if len(out) > 80 {
		return append(out[:80], fmt.Sprintf("…and %d more", len(out)-80))
	}
	return out
}

// CompoundPartialFailureMessage is the merchant-facing summary when some
// steps succeeded and a later step failed.
func CompoundPartialFailureMessage(progress CompoundProgress, cause error) string {
	var b strings.Builder
	if len(progress.Completed) > 0 {
		parts := make([]string, 0, len(progress.Completed))
		for _, s := range progress.Completed {
			parts = append(parts, s.Label+" succeeded")
		}
		b.WriteString(strings.Join(parts, ". "))
		b.WriteString(". ")
	}
	if progress.Failed != nil {
		b.WriteString(progress.Failed.Label)
		b.WriteString(" needs another attempt")
		if cause != nil {
			low := strings.ToLower(cause.Error())
			switch {
			case errors.Is(cause, context.DeadlineExceeded) || strings.Contains(low, "deadline exceeded"):
				// Parent generation wall budget — keep completed-step wording.
				b.Reset()
				if len(progress.Completed) > 0 {
					parts := make([]string, 0, len(progress.Completed))
					for _, s := range progress.Completed {
						parts = append(parts, s.Label)
					}
					fmt.Fprintf(&b, "Generation timed out after %d minutes. %s completed successfully; remaining steps were not completed.",
						ParentGenerationTimeoutMinutes, strings.Join(parts, "; "))
				} else {
					fmt.Fprintf(&b, "Generation timed out after %d minutes. Please try again.", ParentGenerationTimeoutMinutes)
				}
				return b.String()
			case strings.Contains(low, "proposal/tool contract"), strings.Contains(low, "page_registry_entry only registers"):
				b.WriteString(" because of a proposal/tool contract mismatch")
			case strings.Contains(low, "didn't pass validation"), strings.Contains(low, "validation"):
				b.WriteString(" because validation failed")
			case strings.Contains(low, "incomplete atomic"), strings.Contains(low, "incomplete multi-page"):
				b.WriteString(" because the page was not fully created/registered")
			case strings.Contains(low, "menu operation"), strings.Contains(low, "defaults.json invalid"):
				b.WriteString(" because the navigation update could not be completed")
			case strings.Contains(low, "timeout"), strings.Contains(low, "deadline"):
				b.WriteString(" because the provider timed out")
			case strings.Contains(low, "cancel"):
				b.WriteString(" because generation was cancelled")
			default:
				b.WriteString(" because the step could not finish")
			}
		}
		if len(progress.Completed) > 0 {
			b.WriteString(". Successful earlier pages were kept — please retry for the remaining step.")
		} else {
			b.WriteString(". Please try again.")
		}
		return b.String()
	}
	if cause != nil {
		return cause.Error()
	}
	return "Compound generation failed."
}

// compoundStepPreparedPrompt wraps a step's focused instructions with a
// minimal pages.json excerpt (and prior-step note) — never packs blog.liquid.
func compoundStepPreparedPrompt(step CompoundStep, original, pagesJSONExcerpt string, prior *ai.Result) string {
	var b strings.Builder
	b.WriteString("## Atomic compound step (do not expand scope)\n")
	fmt.Fprintf(&b, "Step %d (%s): %s\n", step.ID, step.Kind, step.Label)
	b.WriteString(step.FocusedPrompt)
	b.WriteString("\n\n")
	if prior != nil && len(prior.Files) > 0 {
		b.WriteString("Already completed this turn (do NOT recreate these files):\n")
		for _, f := range prior.Files {
			fmt.Fprintf(&b, "- %s (%s)\n", f.Path, f.Action)
		}
		b.WriteString("\n")
	}
	if strings.TrimSpace(pagesJSONExcerpt) != "" {
		// Show slug list only — never a truncated full pages.json body.
		// Feeding a truncated registry caused the model to rewrite pages.json
		// and drop hundreds of existing entries (−500 line diffs).
		b.WriteString("Existing page identities (already registered — do NOT recreate; do NOT rewrite pages.json):\n")
		for _, id := range pageIdentitiesFromJSON(pagesJSONExcerpt) {
			fmt.Fprintf(&b, "- %s\n", id)
		}
		b.WriteString("\n")
	}
	b.WriteString("---\nMerchant request: ")
	b.WriteString(original)
	return b.String()
}

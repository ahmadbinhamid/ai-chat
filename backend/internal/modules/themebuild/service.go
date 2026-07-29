package themebuild

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/themecheck"
	"ai-chat/internal/themefs"

	"github.com/google/uuid"
)

const (
	pathPagesJSON   = "pages.json"
	pathLayoutStart = "liquid/layout-start.liquid"
	pathLayoutEnd   = "liquid/layout-end.liquid"

	// ChatType is the chat.Chat "type" this module owns — the chat package
	// itself is generic (see its doc comment); "builder" is what scopes a
	// tenant's theme-builder thread apart from any future, unrelated chat
	// use case sharing the same tenant.
	ChatType = "builder"

	// generateTimeout bounds Generate's own work once it's decided to
	// proceed (see workContext) — independent of the caller's request
	// context, but not unbounded either. Matches the HTTP server's own
	// writeTimeout (cmd/server/main.go).
	generateTimeout = 5 * time.Minute

	// maxThemeCheckRetries bounds how many times a proposal themecheck
	// rejects is sent back to the model with its findings before doGenerate
	// gives up — up to maxThemeCheckRetries+1 total Generate calls (the
	// original attempt plus this many retries).
	maxThemeCheckRetries = 2

	pathDefaultsJSON = "defaults.json"
)

// generator is the subset of *ai.Generator's behavior Service depends on —
// letting tests substitute a fake that never calls the real Claude API,
// which matters most for checkAndRepair's retry loop (multiple Generate
// calls per turn). *ai.Generator satisfies this today with no changes on
// its side; callers passing one continue to work unchanged.
type generator interface {
	Generate(ctx context.Context, tc ai.ThemeContext, history []ai.Turn, prompt string, onDelta func(string)) (*ai.Result, error)
}

// Service is the AI theme builder's orchestration: turn a prompt into
// proposed changes and write them straight to the real theme filesystem
// (see Generate) — there is no separate review/apply step.
type Service struct {
	repo        *Repository
	chats       *chat.Service
	gen         generator
	store       *themefs.Store
	themeLocks  *keyedMutex
	generations *generationTracker
}

func NewService(repo *Repository, chats *chat.Service, gen *ai.Generator, store *themefs.Store) *Service {
	return &Service{
		repo:        repo,
		chats:       chats,
		gen:         gen,
		store:       store,
		themeLocks:  newKeyedMutex(),
		generations: newGenerationTracker(),
	}
}

// ErrGenerationInProgress means the tenant's chat already has a background
// generation running — see generationTracker. The caller should reject the
// new request (409) rather than starting a second concurrent Claude call
// for the same chat, which would race the first one's disk writes and
// double-bill the tenant for one prompt.
var ErrGenerationInProgress = errors.New("a generation is already in progress for this chat")

// GenerateInput is one merchant prompt, always against the tenant's one
// ongoing "builder" chat (see chat.Service.GetOrCreateChat).
type GenerateInput struct {
	TenantID uint64
	UserID   *uint64
	UserName string
	// Token is the caller's own bearer token, forwarded to flowpos-backend's
	// theme-file API (see internal/themefs.Store) so every read/write acts
	// as this same user, subject to flowpos-backend's own ownership checks.
	Token     string
	ThemeSlug string
	Prompt    string
}

// GenerateOutcome is the immediate (synchronous) result of accepting a
// prompt: the chat and the user's own recorded message. AssistantMessage
// and Files are always nil here — Generate now returns as soon as the
// prompt is recorded and a background generation has been kicked off (see
// Generate's doc comment), not once Claude has actually replied. The real
// outcome (a new assistant message, generated files, or an error) arrives
// later — the caller polls GET /chat, which reports whether a generation
// is still running (see GenerationStatus) and surfaces the new history once
// it's done.
type GenerateOutcome struct {
	Chat             chat.Chat
	UserMessage      chat.Message
	AssistantMessage *chat.Message
	Files            []GeneratedFile
}

// Generate resolves (or creates) the chat, records the prompt, and returns
// immediately — the actual Claude call, proposal validation, and (if the
// model proposed changes) write to the real theme filesystem all happen in
// a background goroutine (see runGeneration), not before this returns.
//
// This is deliberately async, not a synchronous call the client awaits:
// a full generation can legitimately take several minutes, and no
// intermediary in a real deployment — a CDN proxy, a corporate firewall, a
// flaky mobile connection, even the browser backgrounding the tab — can be
// trusted to keep one HTTP request alive that long. Every request this
// service handles now finishes in milliseconds; the caller learns the
// actual result by polling GET /chat's `generating`/`generation_error`
// fields (see GenerationStatus) instead of waiting on this call's response.
//
// A model/infra failure, a rejected proposal, or a failure while writing to
// the theme is never persisted as a chat turn — errors are request-scoped,
// not chat history (see generationTracker) — same reasoning as before this
// became async, just recorded in memory instead of in the HTTP response.
func (s *Service) Generate(ctx context.Context, in GenerateInput) (GenerateOutcome, error) {
	if in.ThemeSlug == "" {
		return GenerateOutcome{}, errors.New("theme_slug is required")
	}

	c, err := s.chats.GetOrCreateChat(ctx, in.TenantID, ChatType)
	if err != nil {
		return GenerateOutcome{}, err
	}

	if !s.generations.start(c.ID) {
		return GenerateOutcome{}, ErrGenerationInProgress
	}

	userMsg, err := s.chats.RecordUserMessage(ctx, c, in.UserID, in.UserName, in.Prompt)
	if err != nil {
		s.generations.finish(c.ID, nil) // release the slot — nothing actually started
		return GenerateOutcome{}, fmt.Errorf("record user message: %w", err)
	}

	// Detached from the caller's own request lifecycle (which is about to
	// end the moment this function returns) but not unbounded: bounded by
	// generateTimeout, matching the HTTP server's own writeTimeout.
	go s.runGeneration(context.WithoutCancel(ctx), in, c)

	return GenerateOutcome{Chat: c, UserMessage: userMsg}, nil
}

// runGeneration is Generate's actual work, run in the background — see
// Generate's doc comment. Any error here is only ever recorded in the
// in-memory generationTracker (see GenerationStatus), never the database:
// errors are transient/request-scoped, not chat history, the same rule as
// before this became async.
func (s *Service) runGeneration(ctx context.Context, in GenerateInput, c chat.Chat) {
	workCtx, cancel := context.WithTimeout(ctx, generateTimeout)
	defer cancel()

	err := s.doGenerate(workCtx, in, c)
	s.generations.finish(c.ID, err)
}

// doGenerate is the part of generation that used to be Generate's entire
// body before it became async: ask Claude for the resulting file changes
// and — unlike an earlier pending/apply design — write them to the real
// theme filesystem immediately, with no separate "Apply to theme" step.
func (s *Service) doGenerate(ctx context.Context, in GenerateInput, c chat.Chat) error {
	storeAuth := themefs.RequestAuth{Token: in.Token, TenantID: in.TenantID}

	priorMessages, err := s.chats.ListMessages(ctx, in.TenantID, c.ID)
	if err != nil {
		return fmt.Errorf("load chat history: %w", err)
	}

	tc, err := s.buildThemeContext(ctx, storeAuth, in.ThemeSlug)
	if err != nil {
		return fmt.Errorf("load theme context: %w", err)
	}
	tc.EditingFiles, err = s.buildEditingFilesContext(ctx, storeAuth, c.ID)
	if err != nil {
		return fmt.Errorf("load editing-files context: %w", err)
	}

	turns := toTurns(priorMessages)
	result, genErr := s.gen.Generate(ctx, tc, turns, in.Prompt, nil)
	if genErr != nil {
		return genErr
	}

	clearIfNeedsClarification(result)
	if err := validateProposal(result); err != nil {
		return fmt.Errorf("invalid model proposal: %w", err)
	}

	var warnings []themecheck.Finding
	if proposalHasChanges(result) {
		snap, err := s.buildSnapshot(ctx, storeAuth)
		if err != nil {
			return fmt.Errorf("build theme snapshot: %w", err)
		}
		result, warnings, err = s.checkAndRepair(ctx, in, tc, turns, result, snap)
		if err != nil {
			return err
		}
	}

	hasChanges := proposalHasChanges(result)

	var written []writtenFile
	if hasChanges {
		// Serializes the read-modify-write critical section below per
		// theme: two concurrent requests for the same theme (two tabs, a
		// client retry racing the original) must not interleave reads and
		// writes of pages.json/the layout files, or one's registration can
		// silently clobber the other's (a lost update). Scoped tightly to
		// just this section, not the whole call — the Claude call above
		// can take minutes, and a second tab's edit shouldn't queue behind
		// that, only behind the fast disk work.
		unlock := s.themeLocks.Lock(in.ThemeSlug)
		defer unlock()

		// Computed entirely in memory first, nothing written yet: a
		// failure here (e.g. a duplicate page slug) leaves the real theme
		// completely untouched, rather than a validation error arriving
		// after some files already landed on disk with nothing recording
		// that they did.
		plan, err := s.buildWritePlan(ctx, storeAuth, result)
		if err != nil {
			return fmt.Errorf("apply to theme: %w", err)
		}
		written, err = s.commitWritePlan(ctx, storeAuth, plan)
		if err != nil {
			return fmt.Errorf("apply to theme: %w", err)
		}
	}

	applyStatus := chat.ApplyStatusNotApplicable
	if hasChanges {
		applyStatus = chat.ApplyStatusApplied
	}

	// The schema requires "summary" as a key but not a non-empty one, so an
	// empty string is a valid (if unhelpful) reply the model can return. A
	// "completed" turn with empty content isn't just a bland reply, though —
	// it's a landmine: internal/ai applies a prompt-cache breakpoint to the
	// last history turn on every subsequent call, and Anthropic rejects
	// cache_control on an empty text block outright (400), which would take
	// down every future message in this chat, not just this one. Never
	// persist that state.
	summary := result.Summary
	if summary == "" {
		summary = "Done."
	}
	summary = appendWarningsNote(summary, warnings)

	assistantMsg, err := s.chats.RecordAssistantMessage(ctx, c, summary, chat.MessageStatusCompleted, result.InputTokens, result.OutputTokens, applyStatus)
	if err != nil {
		return fmt.Errorf("record assistant message: %w", err)
	}

	if _, err := s.persistFileRecords(ctx, c, assistantMsg.ID, written); err != nil {
		return fmt.Errorf("persist generated-file audit rows: %w", err)
	}

	return nil
}

// GenerationStatus reports whether chatID currently has a background
// Generate call running, and the error from the most recently finished one
// if it failed (cleared as soon as the next generation starts) — see
// generationTracker. Purely in-memory: restarting this process (a deploy)
// loses in-flight status, same tradeoff already accepted for the rate
// limiter and per-theme locks.
func (s *Service) GenerationStatus(chatID string) (generating bool, errMsg string) {
	return s.generations.get(chatID)
}

func (s *Service) buildThemeContext(ctx context.Context, storeAuth themefs.RequestAuth, themeSlug string) (ai.ThemeContext, error) {
	pagesJSON, err := s.store.ReadFile(ctx, storeAuth, pathPagesJSON)
	if err != nil {
		return ai.ThemeContext{}, err
	}
	defaultsJSON, err := s.store.ReadFile(ctx, storeAuth, pathDefaultsJSON)
	if err != nil {
		return ai.ThemeContext{}, err
	}
	return ai.ThemeContext{
		ThemeSlug:    themeSlug,
		PagesJSON:    pagesJSON,
		DefaultsJSON: defaultsJSON,
	}, nil
}

// buildSnapshot fetches the current theme's full file-path listing (every
// path that exists, for rule 4's render-target-exists check — see
// themecheck.Snapshot.Paths) plus real content for the handful of files
// themecheck actually reads (pages.json, defaults.json, the two layout
// files). Called once per doGenerate call, before the check-and-repair
// loop: nothing is written to the theme until after that loop accepts a
// proposal, so the same snapshot is valid across every retry within one
// call — no need to refetch it per attempt.
func (s *Service) buildSnapshot(ctx context.Context, storeAuth themefs.RequestAuth) (themecheck.Snapshot, error) {
	tree, err := s.store.ListFiles(ctx, storeAuth)
	if err != nil {
		return themecheck.Snapshot{}, fmt.Errorf("list theme files: %w", err)
	}
	paths := make(map[string]bool)
	flattenFileTree(tree, paths)

	files := make(map[string]string, 4)
	for _, path := range []string{pathPagesJSON, pathDefaultsJSON, pathLayoutStart, pathLayoutEnd} {
		content, err := s.store.ReadFile(ctx, storeAuth, path)
		if err != nil {
			return themecheck.Snapshot{}, fmt.Errorf("read %s: %w", path, err)
		}
		files[path] = content
	}

	return themecheck.Snapshot{Files: files, Paths: paths}, nil
}

// flattenFileTree walks a theme's file tree (see themefs.Store.ListFiles),
// recording every FILE path (not directories) into paths.
func flattenFileTree(entries []themefs.FileTreeEntry, paths map[string]bool) {
	for _, e := range entries {
		if e.Type == "file" {
			paths[e.Path] = true
		}
		if len(e.Children) > 0 {
			flattenFileTree(e.Children, paths)
		}
	}
}

// toProposal maps ai.Result into the minimal shape themecheck.Check needs —
// PageRegistryEntry carries over unchanged since ai.Result already types it
// as *themefs.PageEntry (see themecheck.Proposal's doc comment).
func toProposal(r *ai.Result) themecheck.Proposal {
	files := make([]themecheck.ProposedFile, len(r.Files))
	for i, f := range r.Files {
		files[i] = themecheck.ProposedFile{Path: f.Path, Action: f.Action, Content: f.Content}
	}
	return themecheck.Proposal{
		Files:              files,
		PageRegistryEntry:  r.PageRegistryEntry,
		LayoutLinksToAdd:   r.LayoutLinksToAdd,
		LayoutScriptsToAdd: r.LayoutScriptsToAdd,
	}
}

// proposalHasChanges reports whether result proposes anything to write —
// shared by doGenerate (deciding whether to run themecheck/buildWritePlan at
// all) and its post-repair recheck (a retry that ends in NeedsClarification
// legitimately has nothing left to write).
func proposalHasChanges(result *ai.Result) bool {
	return len(result.Files) > 0 || result.PageRegistryEntry != nil ||
		len(result.LayoutLinksToAdd) > 0 || len(result.LayoutScriptsToAdd) > 0
}

// clearIfNeedsClarification defensively drops any changes the model
// proposed despite the system prompt's instruction not to when asking a
// clarifying question — a clarification turn has nothing to apply.
func clearIfNeedsClarification(result *ai.Result) {
	if !result.NeedsClarification {
		return
	}
	result.Files = nil
	result.PageRegistryEntry = nil
	result.LayoutLinksToAdd = nil
	result.LayoutScriptsToAdd = nil
}

// checkAndRepair validates result against snap via themecheck.Check. A
// blocking (error-severity) finding is fed back to the model as a new
// assistant/user turn pair and retried, up to maxThemeCheckRetries times;
// a proposal that never passes fails the generation with a merchant-
// friendly message. Token usage from every retry is folded into the
// accepted result's totals, so RecordAssistantMessage still bills/records
// the full cost of this turn, not just its last attempt. The accepted
// result's warning findings are returned alongside it — never blocking,
// just surfaced (see doGenerate's appendWarningsNote).
func (s *Service) checkAndRepair(
	ctx context.Context,
	in GenerateInput,
	tc ai.ThemeContext,
	history []ai.Turn,
	result *ai.Result,
	snap themecheck.Snapshot,
) (*ai.Result, []themecheck.Finding, error) {
	turns := append([]ai.Turn(nil), history...)
	totalInput, totalOutput := result.InputTokens, result.OutputTokens

	for attempt := 1; ; attempt++ {
		findings := themecheck.Check(toProposal(result), snap)
		errorFindings, warningFindings := splitFindings(findings)

		if len(errorFindings) == 0 {
			if attempt > 1 {
				slog.Info("themecheck accepted proposal after retry",
					"tenant_id", in.TenantID, "theme_slug", in.ThemeSlug, "attempt", attempt, "warning_count", len(warningFindings))
			}
			result.InputTokens, result.OutputTokens = totalInput, totalOutput
			return result, warningFindings, nil
		}

		slog.Warn("themecheck rejected proposal",
			"tenant_id", in.TenantID, "theme_slug", in.ThemeSlug, "attempt", attempt,
			"error_count", len(errorFindings), "rules", findingRules(errorFindings))

		if attempt > maxThemeCheckRetries {
			return nil, nil, fmt.Errorf("the generated changes didn't pass validation after %d attempts: %s",
				attempt, summarizeFindings(errorFindings))
		}

		turns = append(turns, ai.Turn{Role: "assistant", Content: recapAssistantTurn(result)})
		repair := repairPrompt(errorFindings)

		retried, genErr := s.gen.Generate(ctx, tc, turns, repair, nil)
		if genErr != nil {
			return nil, nil, fmt.Errorf("retry generation: %w", genErr)
		}
		totalInput += retried.InputTokens
		totalOutput += retried.OutputTokens
		turns = append(turns, ai.Turn{Role: "user", Content: repair})

		clearIfNeedsClarification(retried)
		if err := validateProposal(retried); err != nil {
			return nil, nil, fmt.Errorf("invalid model proposal (retry %d): %w", attempt, err)
		}
		result = retried
	}
}

func splitFindings(findings []themecheck.Finding) (errorFindings, warningFindings []themecheck.Finding) {
	for _, f := range findings {
		if f.Severity == themecheck.SeverityError {
			errorFindings = append(errorFindings, f)
		} else {
			warningFindings = append(warningFindings, f)
		}
	}
	return errorFindings, warningFindings
}

func findingRules(findings []themecheck.Finding) []string {
	rules := make([]string, len(findings))
	for i, f := range findings {
		rules[i] = f.Rule
	}
	return rules
}

func summarizeFindings(findings []themecheck.Finding) string {
	parts := make([]string, len(findings))
	for i, f := range findings {
		if f.Path != "" {
			parts[i] = fmt.Sprintf("[%s] %s: %s", f.Rule, f.Path, f.Message)
		} else {
			parts[i] = fmt.Sprintf("[%s] %s", f.Rule, f.Message)
		}
	}
	return strings.Join(parts, "; ")
}

// recapAssistantTurn replays a rejected proposal's file content back to the
// model as its own prior turn. Without this the model retrying would have
// no memory of what it just wrote: a rejected proposal is never written to
// disk or the chat_generated_files audit trail (Check runs before
// buildWritePlan), so buildEditingFilesContext's real-file grounding won't
// have it either.
func recapAssistantTurn(result *ai.Result) string {
	var b strings.Builder
	if result.Summary != "" {
		fmt.Fprintf(&b, "%s\n\n", result.Summary)
	}
	for _, f := range result.Files {
		fmt.Fprintf(&b, "### %s (%s)\n%s\n\n", f.Path, f.Action, f.Content)
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		out = "(no files proposed)"
	}
	return out
}

// repairPrompt is the new user turn sent back to the model after a rejected
// proposal — every error finding, since those are what actually blocked the
// write (warnings are surfaced to the merchant, never fed back for a retry).
func repairPrompt(errorFindings []themecheck.Finding) string {
	var b strings.Builder
	b.WriteString("Your last proposal failed validation against the theme engine spec. Fix these specific problems " +
		"and resubmit the complete corrected set of files (not a diff):\n\n")
	for _, f := range errorFindings {
		if f.Path != "" {
			fmt.Fprintf(&b, "- [%s] %s: %s\n", f.Rule, f.Path, f.Message)
		} else {
			fmt.Fprintf(&b, "- [%s] %s\n", f.Rule, f.Message)
		}
	}
	return b.String()
}

// appendWarningsNote appends a short, merchant-readable note listing any
// warning-severity findings to summary. Rides on the existing summary text
// rather than a new chat_messages column — see phase 1 wiring notes; phase
// 3's generation_events log is the intended home for this once it exists.
func appendWarningsNote(summary string, warnings []themecheck.Finding) string {
	if len(warnings) == 0 {
		return summary
	}
	var b strings.Builder
	b.WriteString(summary)
	fmt.Fprintf(&b, "\n\nNote: %d warning(s):", len(warnings))
	for _, f := range warnings {
		if f.Path != "" {
			fmt.Fprintf(&b, "\n- %s: %s", f.Path, f.Message)
		} else {
			fmt.Fprintf(&b, "\n- %s", f.Message)
		}
	}
	return b.String()
}

// maxEditingFiles bounds how many distinct files' full content get sent as
// grounding on every call — this block isn't cached (it changes per request),
// so an unbounded chat history touching dozens of pages would otherwise cost
// more tokens on every single turn as the chat gets older.
const maxEditingFiles = 25

// maxEditingFileBytes caps a single file's contribution to that grounding.
// Liquid pages/components are normally a few KB; this only bites a
// pathological outlier, and truncating (with a clear marker) rather than
// silently sending a full huge file keeps a single oversized page from
// dominating the prompt's token budget.
const maxEditingFileBytes = 40_000

// buildEditingFilesContext gathers every distinct path this chat has ever
// written a file for, and reads each one's real on-disk content — so an
// incremental request ("add a subtitle to our-story") gets the actual
// current file to edit. Only the maxEditingFiles most recently touched
// paths are kept, most recent first by touch order, so a long-running
// chat's context stays bounded.
func (s *Service) buildEditingFilesContext(ctx context.Context, storeAuth themefs.RequestAuth, chatID string) (map[string]string, error) {
	files, err := s.repo.ListFilesByChat(ctx, chatID)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, nil
	}

	// files is ordered oldest-first; walking it in order and overwriting per
	// path keeps both the latest content for that path and the index of its
	// most recent touch, so trimming to maxEditingFiles below keeps the
	// paths the merchant is still actively iterating on.
	type touched struct {
		content string
		lastIdx int
	}
	latest := make(map[string]touched, len(files))
	for i, f := range files {
		latest[f.FilePath] = touched{content: f.Content, lastIdx: i}
	}

	paths := make([]string, 0, len(latest))
	for path := range latest {
		paths = append(paths, path)
	}
	sort.Slice(paths, func(i, j int) bool { return latest[paths[i]].lastIdx > latest[paths[j]].lastIdx })
	if len(paths) > maxEditingFiles {
		paths = paths[:maxEditingFiles]
	}

	editing := make(map[string]string, len(paths))
	for _, path := range paths {
		current, err := s.store.ReadFile(ctx, storeAuth, path)
		if err != nil {
			return nil, err
		}
		content := current
		if content == "" {
			content = latest[path].content
		}
		if len(content) > maxEditingFileBytes {
			truncated := truncateAtRuneBoundary(content, maxEditingFileBytes)
			content = truncated + fmt.Sprintf("\n<!-- truncated: %d more bytes omitted -->", len(content)-len(truncated))
		}
		editing[path] = content
	}
	return editing, nil
}

// truncateAtRuneBoundary truncates s to at most maxBytes bytes, backing up
// to the nearest rune boundary if the naive cut point would split a
// multi-byte UTF-8 character — generated theme content (page copy, brand
// names) can legitimately contain non-ASCII text, and a raw byte slice here
// could otherwise hand the model invalid UTF-8.
func truncateAtRuneBoundary(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// toTurns replays a chat's history as message turns for the model. Anthropic
// rejects an empty text content block outright ("text content blocks must
// be non-empty") — not just for the cache_control breakpoint, for any
// message anywhere in the request — so an empty turn is skipped rather than
// replayed, regardless of role or status. This also self-heals any chat
// that already has an empty "completed" turn sitting in its history from
// before the fix that stops persisting one (see Generate): the bad row
// stays in the database, but it's excluded here every time history gets
// rebuilt, so it can't keep breaking every future message in that chat.
func toTurns(messages []chat.Message) []ai.Turn {
	turns := make([]ai.Turn, 0, len(messages))
	for _, m := range messages {
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		switch m.Role {
		case chat.RoleUser:
			turns = append(turns, ai.Turn{Role: "user", Content: m.Content})
		case chat.RoleAssistant:
			if m.Status == chat.MessageStatusCompleted {
				turns = append(turns, ai.Turn{Role: "assistant", Content: m.Content})
			}
		}
	}
	return turns
}

// validateProposal re-checks every path the model proposed against the same
// rules internal/ai's system prompt already asked it to follow — defense in
// depth against a model mistake, never trusting model output as
// automatically safe just because it was asked nicely.
func validateProposal(r *ai.Result) error {
	for _, f := range r.Files {
		// Not re-embedding f.Path here: themefs' error already includes a
		// bounded preview of it. A proposal gone badly wrong can put an
		// entire file's content where a path belongs, and doubling that
		// blob into an outer wrapper is exactly the duplication that made
		// an earlier version of this error unreadable (and huge) in the
		// chat UI.
		if err := themefs.ValidateGeneratedFilePath(f.Path); err != nil {
			return fmt.Errorf("proposed file rejected: %w", err)
		}
		if f.Action != "create" && f.Action != "update" {
			return fmt.Errorf("file %q: invalid action %q", f.Path, f.Action)
		}
	}
	for _, p := range r.LayoutLinksToAdd {
		if err := themefs.ValidateGeneratedFilePath(p); err != nil {
			return fmt.Errorf("proposed layout css link rejected: %w", err)
		}
	}
	for _, p := range r.LayoutScriptsToAdd {
		if err := themefs.ValidateGeneratedFilePath(p); err != nil {
			return fmt.Errorf("proposed layout js link rejected: %w", err)
		}
	}
	return nil
}

// writtenFile is one proposed file after it's been written to disk, paired
// with whatever content it replaced — captured before the write so the
// audit row persisted afterward (see persistFileRecords) can still record
// what changed, even though the real "before" state is gone from disk by then.
type writtenFile struct {
	generated ai.GeneratedFile
	previous  *string
}

// planFile is one file a writePlan will commit — either a proposed file
// (action/content straight from the model) or the recomputed content of a
// shared file (a layout file) after folding in this turn's splice. previous
// is only meaningful for proposed files (see writtenFile) and is nil
// otherwise. pageMeta is set only when this file is the page.liquid file
// PageRegistryEntry describes — flowpos-backend's own theme-file API upserts
// pages.json itself from these fields (see themefs.Store.WriteFile), so this
// service no longer computes pages.json content directly.
type planFile struct {
	path     string
	action   FileAction
	content  string
	previous *string
	pageMeta *themefs.PageMeta
}

// writePlan is everything one turn needs to commit to the real theme,
// computed entirely in memory before anything is written — see
// buildWritePlan/commitWritePlan.
type writePlan struct {
	files       []planFile
	layoutStart *planFile
	layoutEnd   *planFile
}

// buildWritePlan computes every file this turn would write — proposed
// files verbatim (with page metadata attached to the one matching
// PageRegistryEntry, if any), plus the layout-file splices — using only
// reads, never a write. Nothing is committed until commitWritePlan runs, so
// a failure here (a layout file missing its insertion marker, a page
// registry entry with no matching file) leaves the real theme completely
// untouched instead of partially, silently modified.
func (s *Service) buildWritePlan(ctx context.Context, storeAuth themefs.RequestAuth, result *ai.Result) (writePlan, error) {
	var plan writePlan

	for _, f := range result.Files {
		previous, err := s.store.ReadFile(ctx, storeAuth, f.Path)
		if err != nil {
			return writePlan{}, fmt.Errorf("read %q: %w", f.Path, err)
		}
		var previousPtr *string
		if previous != "" {
			previousPtr = &previous
		}
		plan.files = append(plan.files, planFile{
			path:     f.Path,
			action:   FileAction(f.Action),
			content:  f.Content,
			previous: previousPtr,
		})
	}

	if result.PageRegistryEntry != nil {
		entry := result.PageRegistryEntry
		// entry.Path is the route prefix ("/pages" or "/pages/auth"), never a
		// file path — the proposed file's actual theme-relative path has to be
		// derived from it the same way §5 requires: page == the .liquid file's
		// basename.
		wantPath := "pages/" + entry.Page + ".liquid"
		if entry.Path == "/pages/auth" {
			wantPath = "pages/auth/" + entry.Page + ".liquid"
		}
		matched := false
		for i := range plan.files {
			if plan.files[i].path != wantPath {
				continue
			}
			plan.files[i].pageMeta = &themefs.PageMeta{
				Title:          entry.Title,
				Slug:           entry.Slug,
				Type:           entry.Type,
				Status:         entry.Status,
				SEOTitle:       entry.SEOTitle,
				SEODescription: entry.SEODescription,
				SEOKeywords:    entry.SEOKeywords,
				OGTitle:        entry.OGTitle,
				OGDescription:  entry.OGDescription,
				OGImagePath:    entry.OGImagePath,
			}
			matched = true
			break
		}
		if !matched {
			return writePlan{}, fmt.Errorf("register page: page_registry_entry (page %q, path %q) has no matching proposed file at %q", entry.Page, entry.Path, wantPath)
		}
	}

	if len(result.LayoutLinksToAdd) > 0 {
		current, err := s.store.ReadFile(ctx, storeAuth, pathLayoutStart)
		if err != nil {
			return writePlan{}, fmt.Errorf("add layout css links: %w", err)
		}
		changedAny := false
		for _, path := range result.LayoutLinksToAdd {
			updated, changed, err := themefs.AddStylesheetLink(current, path)
			if err != nil {
				return writePlan{}, fmt.Errorf("add layout css link %q: %w", path, err)
			}
			if changed {
				current = updated // so a second link in the same turn splices against the first
				changedAny = true
			}
		}
		if changedAny {
			plan.layoutStart = &planFile{path: pathLayoutStart, content: current}
		}
	}

	if len(result.LayoutScriptsToAdd) > 0 {
		current, err := s.store.ReadFile(ctx, storeAuth, pathLayoutEnd)
		if err != nil {
			return writePlan{}, fmt.Errorf("add layout js links: %w", err)
		}
		changedAny := false
		for _, path := range result.LayoutScriptsToAdd {
			updated, changed, err := themefs.AddDeferredScript(current, path)
			if err != nil {
				return writePlan{}, fmt.Errorf("add layout js link %q: %w", path, err)
			}
			if changed {
				current = updated
				changedAny = true
			}
		}
		if changedAny {
			plan.layoutEnd = &planFile{path: pathLayoutEnd, content: current}
		}
	}

	return plan, nil
}

// commitWritePlan writes everything in plan through flowpos-backend's own
// theme-file API (each individual write already atomic on its side — see
// themefs.Store.WriteFile). Only the proposed files (plan.files) get an
// audit trail (see persistFileRecords) — the layout files are shared,
// structurally-spliced config, not "generated files" in their own right.
func (s *Service) commitWritePlan(ctx context.Context, storeAuth themefs.RequestAuth, plan writePlan) ([]writtenFile, error) {
	written := make([]writtenFile, 0, len(plan.files))
	for _, f := range plan.files {
		if err := s.store.WriteFile(ctx, storeAuth, f.path, f.content, f.pageMeta); err != nil {
			return written, fmt.Errorf("write %q: %w", f.path, err)
		}
		written = append(written, writtenFile{
			generated: ai.GeneratedFile{Path: f.path, Action: string(f.action), Content: f.content},
			previous:  f.previous,
		})
	}
	for _, f := range []*planFile{plan.layoutStart, plan.layoutEnd} {
		if f == nil {
			continue
		}
		if err := s.store.WriteFile(ctx, storeAuth, f.path, f.content, nil); err != nil {
			return written, fmt.Errorf("write %q: %w", f.path, err)
		}
	}
	return written, nil
}

// persistFileRecords writes the audit row for each file already written to
// disk (see commitWritePlan) — done after the assistant message exists
// since chat_generated_files.message_id is a foreign key into it.
func (s *Service) persistFileRecords(ctx context.Context, c chat.Chat, messageID string, written []writtenFile) ([]GeneratedFile, error) {
	files := make([]GeneratedFile, 0, len(written))
	now := time.Now().UTC()
	for _, w := range written {
		f := GeneratedFile{
			ID:              uuid.NewString(),
			MessageID:       messageID,
			ChatID:          c.ID,
			FilePath:        w.generated.Path,
			Action:          FileAction(w.generated.Action),
			Language:        languageFor(w.generated.Path),
			Content:         w.generated.Content,
			PreviousContent: w.previous,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if err := s.repo.CreateFile(ctx, f); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, nil
}

// FilesForChat returns every generated file ever written across a chat's
// whole history — used to hydrate GET /chat so reopening the page still
// shows each turn's "Generated files" card, not just the most recent one.
// Does not check ownership itself; the caller (the chat handler) has
// already scoped the chat to the requesting tenant.
func (s *Service) FilesForChat(ctx context.Context, chatID string) ([]GeneratedFile, error) {
	return s.repo.ListFilesByChat(ctx, chatID)
}

func languageFor(path string) string {
	switch {
	case strings.HasSuffix(path, ".liquid"):
		return "LIQUID"
	case strings.HasSuffix(path, ".css"):
		return "CSS"
	case strings.HasSuffix(path, ".js"):
		return "JS"
	default:
		return ""
	}
}

// keyedMutex hands out one mutex per key, created lazily — same shape as
// ratelimit.PerTenantLimiter, minus the token-bucket part. The map grows by
// one entry per distinct theme_slug ever seen by this process, not per
// request — bounded by how many themes actually exist, unlike a per-token
// cache, so no sweep/eviction is needed here.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newKeyedMutex() *keyedMutex {
	return &keyedMutex{locks: make(map[string]*sync.Mutex)}
}

// Lock blocks until key's lock is held, and returns the func to release it.
func (k *keyedMutex) Lock(key string) func() {
	k.mu.Lock()
	lock, ok := k.locks[key]
	if !ok {
		lock = &sync.Mutex{}
		k.locks[key] = lock
	}
	k.mu.Unlock()

	lock.Lock()
	return lock.Unlock
}

// generationState is one chat's most recent async generation outcome — see
// generationTracker.
type generationState struct {
	generating bool
	err        string
}

// generationTracker holds one generationState per chat ID, purely in
// memory — this is how a caller polling GET /chat learns when a background
// Generate call (see runGeneration) finishes and whether it failed, since
// the original POST /chats/messages already returned long before that
// happens. Never persisted: same "errors are transient, not chat history"
// rule this service already followed before generation became async.
// Bounded by how many chats actually exist (one per tenant), same reasoning
// as keyedMutex, so no sweep/eviction is needed.
type generationTracker struct {
	mu     sync.Mutex
	states map[string]*generationState
}

func newGenerationTracker() *generationTracker {
	return &generationTracker{states: make(map[string]*generationState)}
}

// start marks chatID as generating and returns true, or returns false
// without changing anything if a generation is already in flight for it —
// the caller should reject a second concurrent request for the same chat
// (see ErrGenerationInProgress) rather than racing a redundant Claude call
// against the first one.
func (t *generationTracker) start(chatID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s, ok := t.states[chatID]; ok && s.generating {
		return false
	}
	t.states[chatID] = &generationState{generating: true}
	return true
}

// finish records chatID's generation as no longer running, with err (or
// nil for success) as the outcome a subsequent GenerationStatus call will
// see until the next generation starts.
func (t *generationTracker) finish(chatID string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.states[chatID]
	if s == nil {
		s = &generationState{}
		t.states[chatID] = s
	}
	s.generating = false
	if err != nil {
		s.err = err.Error()
	} else {
		s.err = ""
	}
}

func (t *generationTracker) get(chatID string) (generating bool, errMsg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.states[chatID]
	if s == nil {
		return false, ""
	}
	return s.generating, s.err
}

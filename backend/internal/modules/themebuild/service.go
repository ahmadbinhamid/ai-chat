package themebuild

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
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
)

// Service is the AI theme builder's orchestration: turn a prompt into
// proposed changes and write them straight to the real theme filesystem
// (see Generate) — there is no separate review/apply step.
type Service struct {
	repo       *Repository
	chats      *chat.Service
	gen        *ai.Generator
	store      *themefs.Store
	themeLocks *keyedMutex
}

func NewService(repo *Repository, chats *chat.Service, gen *ai.Generator, store *themefs.Store) *Service {
	return &Service{repo: repo, chats: chats, gen: gen, store: store, themeLocks: newKeyedMutex()}
}

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

// GenerateOutcome is everything a generation call produced, for the handler
// to render back to the client. AssistantMessage is nil when Generate
// returns an error: a failed turn is never persisted (see Generate's doc
// comment), so there is nothing to point it at. Chat and UserMessage are
// still populated on most failures, since the user's own prompt is recorded
// before anything that can fail — the caller can still refresh history to
// show it, even though there's no reply to go with it yet.
type GenerateOutcome struct {
	Chat             chat.Chat
	UserMessage      chat.Message
	AssistantMessage *chat.Message
	Files            []GeneratedFile
}

// Generate resolves (or creates) the chat, records the prompt, asks Claude
// for the resulting file changes, and — unlike an earlier pending/apply
// design — writes them to the real theme filesystem immediately, in the
// same request: there is no "Apply to theme" step for the merchant to
// trigger separately.
//
// A model/infra failure, a rejected proposal, or a failure while writing to
// disk is never persisted as a chat turn — errors are request-scoped, not
// chat history. This is deliberate, not an oversight: an error message is
// only useful at the moment it happens, and a chat shared by everyone on the
// tenant (see chat.Service.GetOrCreateChat) shouldn't accumulate every
// transient failure any of them ever hit as permanent, re-displayed-forever
// history. The caller renders the error from the HTTP response itself
// (ephemeral, e.g. a toast) rather than from anything stored in the
// database — see the frontend's ai-chatbot page for where that's shown.
func (s *Service) Generate(ctx context.Context, in GenerateInput) (GenerateOutcome, error) {
	if in.ThemeSlug == "" {
		return GenerateOutcome{}, errors.New("theme_slug is required")
	}

	// Detached from the caller's own request lifecycle, but not unbounded:
	// once a merchant kicks off a generation, closing the tab or a flaky
	// mobile connection shouldn't sever a multi-minute Claude call and
	// silently orphan whatever it was about to write to the real theme —
	// the outcome should be a real, recorded one either way, not something
	// that depends on the browser staying connected for the whole call.
	workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), generateTimeout)
	defer cancel()

	storeAuth := themefs.RequestAuth{Token: in.Token, TenantID: in.TenantID}

	c, err := s.chats.GetOrCreateChat(workCtx, in.TenantID, ChatType)
	if err != nil {
		return GenerateOutcome{}, err
	}

	priorMessages, err := s.chats.ListMessages(workCtx, in.TenantID, c.ID)
	if err != nil {
		return GenerateOutcome{}, fmt.Errorf("load chat history: %w", err)
	}

	userMsg, err := s.chats.RecordUserMessage(workCtx, c, in.UserID, in.UserName, in.Prompt)
	if err != nil {
		return GenerateOutcome{}, fmt.Errorf("record user message: %w", err)
	}
	// From here on, any failure still returns this much: the user's prompt
	// really was recorded, so the caller has something to refresh to even
	// though there's no assistant reply to go with it.
	outcome := GenerateOutcome{Chat: c, UserMessage: userMsg}

	tc, err := s.buildThemeContext(workCtx, storeAuth, in.ThemeSlug)
	if err != nil {
		return outcome, fmt.Errorf("load theme context: %w", err)
	}
	tc.EditingFiles, err = s.buildEditingFilesContext(workCtx, storeAuth, c.ID)
	if err != nil {
		return outcome, fmt.Errorf("load editing-files context: %w", err)
	}

	result, genErr := s.gen.Generate(workCtx, tc, toTurns(priorMessages), in.Prompt, nil)
	if genErr != nil {
		return outcome, genErr
	}

	if result.NeedsClarification {
		// Defensive: even if the model attached files to a clarification
		// reply despite the system prompt's instruction not to, never write
		// them — a clarification turn has nothing to apply.
		result.Files = nil
		result.PageRegistryEntry = nil
		result.LayoutLinksToAdd = nil
		result.LayoutScriptsToAdd = nil
	}

	if err := validateProposal(result); err != nil {
		return outcome, fmt.Errorf("invalid model proposal: %w", err)
	}

	hasChanges := len(result.Files) > 0 || result.PageRegistryEntry != nil || len(result.LayoutLinksToAdd) > 0 || len(result.LayoutScriptsToAdd) > 0

	var written []writtenFile
	if hasChanges {
		// Serializes the read-modify-write critical section below per
		// theme: two concurrent requests for the same theme (two tabs, a
		// client retry racing the original) must not interleave reads and
		// writes of pages.json/the layout files, or one's registration can
		// silently clobber the other's (a lost update). Scoped tightly to
		// just this section, not the whole request — the Claude call above
		// can take minutes, and a second tab's edit shouldn't queue behind
		// that, only behind the fast disk work.
		unlock := s.themeLocks.Lock(in.ThemeSlug)
		defer unlock()

		// Computed entirely in memory first, nothing written yet: a
		// failure here (e.g. a duplicate page slug) leaves the real theme
		// completely untouched, rather than a validation error arriving
		// after some files already landed on disk with nothing recording
		// that they did.
		plan, err := s.buildWritePlan(workCtx, storeAuth, result)
		if err != nil {
			return outcome, fmt.Errorf("apply to theme: %w", err)
		}
		written, err = s.commitWritePlan(workCtx, storeAuth, plan)
		if err != nil {
			return outcome, fmt.Errorf("apply to theme: %w", err)
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

	assistantMsg, err := s.chats.RecordAssistantMessage(workCtx, c, summary, chat.MessageStatusCompleted, result.InputTokens, result.OutputTokens, applyStatus)
	if err != nil {
		return outcome, fmt.Errorf("record assistant message: %w", err)
	}
	outcome.AssistantMessage = &assistantMsg

	files, err := s.persistFileRecords(workCtx, c, assistantMsg.ID, written)
	if err != nil {
		return outcome, fmt.Errorf("persist generated-file audit rows: %w", err)
	}
	outcome.Files = files

	return outcome, nil
}

func (s *Service) buildThemeContext(ctx context.Context, storeAuth themefs.RequestAuth, themeSlug string) (ai.ThemeContext, error) {
	pagesJSON, err := s.store.ReadFile(ctx, storeAuth, pathPagesJSON)
	if err != nil {
		return ai.ThemeContext{}, err
	}
	defaultsJSON, err := s.store.ReadFile(ctx, storeAuth, "defaults.json")
	if err != nil {
		return ai.ThemeContext{}, err
	}
	return ai.ThemeContext{
		ThemeSlug:    themeSlug,
		PagesJSON:    pagesJSON,
		DefaultsJSON: defaultsJSON,
	}, nil
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
		matched := false
		for i := range plan.files {
			if plan.files[i].path != entry.Path {
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
			return writePlan{}, fmt.Errorf("register page: page_registry_entry.path %q has no matching proposed file", entry.Path)
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

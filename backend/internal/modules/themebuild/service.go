package themebuild

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

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
)

// Service is the AI theme builder's orchestration: turn a prompt into
// proposed changes and write them straight to the real theme filesystem
// (see Generate) — there is no separate review/apply step.
type Service struct {
	repo  *Repository
	chats *chat.Service
	gen   *ai.Generator
	store *themefs.Store
}

func NewService(repo *Repository, chats *chat.Service, gen *ai.Generator, store *themefs.Store) *Service {
	return &Service{repo: repo, chats: chats, gen: gen, store: store}
}

// GenerateInput is one merchant prompt, always against the tenant's one
// ongoing "builder" chat (see chat.Service.GetOrCreateChat).
type GenerateInput struct {
	TenantID  uint64
	UserID    *uint64
	UserName  string
	ThemeSlug string
	Prompt    string
}

// GenerateOutcome is everything a generation call produced, for the handler
// to render back to the client.
type GenerateOutcome struct {
	Chat             chat.Chat
	UserMessage      chat.Message
	AssistantMessage chat.Message
	Files            []GeneratedFile
}

// Generate resolves (or creates) the chat, records the prompt, asks Claude
// for the resulting file changes, and — unlike an earlier pending/apply
// design — writes them to the real theme filesystem immediately, in the
// same request: there is no "Apply to theme" step for the merchant to
// trigger separately. A model/infra failure, or a failure while writing to
// disk, still returns a GenerateOutcome (the failed assistant turn is
// recorded so it shows up in history) alongside a non-nil error — callers
// should render the outcome either way and use the error only to pick the
// HTTP status.
func (s *Service) Generate(ctx context.Context, in GenerateInput) (GenerateOutcome, error) {
	if in.ThemeSlug == "" {
		return GenerateOutcome{}, errors.New("theme_slug is required")
	}
	c, err := s.chats.GetOrCreateChat(ctx, in.TenantID, ChatType)
	if err != nil {
		return GenerateOutcome{}, err
	}

	priorMessages, err := s.chats.ListMessages(ctx, in.TenantID, c.ID)
	if err != nil {
		return GenerateOutcome{}, fmt.Errorf("load chat history: %w", err)
	}

	userMsg, err := s.chats.RecordUserMessage(ctx, c, in.UserID, in.UserName, in.Prompt)
	if err != nil {
		return GenerateOutcome{}, fmt.Errorf("record user message: %w", err)
	}

	tc, err := s.buildThemeContext(in.ThemeSlug)
	if err != nil {
		return GenerateOutcome{}, fmt.Errorf("load theme context: %w", err)
	}
	tc.EditingFiles, err = s.buildEditingFilesContext(ctx, in.ThemeSlug, c.ID)
	if err != nil {
		return GenerateOutcome{}, fmt.Errorf("load editing-files context: %w", err)
	}

	result, genErr := s.gen.Generate(ctx, tc, toTurns(priorMessages), in.Prompt, nil)
	if genErr != nil {
		errMsg := genErr.Error()
		assistantMsg, recErr := s.chats.RecordAssistantMessage(ctx, c, "", chat.MessageStatusFailed, &errMsg, 0, 0, chat.ApplyStatusNotApplicable)
		if recErr != nil {
			return GenerateOutcome{}, fmt.Errorf("record failed turn: %w (generation error: %w)", recErr, genErr)
		}
		return GenerateOutcome{Chat: c, UserMessage: userMsg, AssistantMessage: assistantMsg}, genErr
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
		errMsg := fmt.Sprintf("model proposed an invalid change and was rejected: %v", err)
		assistantMsg, recErr := s.chats.RecordAssistantMessage(ctx, c, "", chat.MessageStatusFailed, &errMsg, result.InputTokens, result.OutputTokens, chat.ApplyStatusNotApplicable)
		if recErr != nil {
			return GenerateOutcome{}, fmt.Errorf("record rejected turn: %w (validation error: %w)", recErr, err)
		}
		return GenerateOutcome{Chat: c, UserMessage: userMsg, AssistantMessage: assistantMsg}, fmt.Errorf("invalid model proposal: %w", err)
	}

	hasChanges := len(result.Files) > 0 || result.PageRegistryEntry != nil || len(result.LayoutLinksToAdd) > 0 || len(result.LayoutScriptsToAdd) > 0

	var written []writtenFile
	if hasChanges {
		written, err = s.applyFilesToDisk(in.ThemeSlug, result.Files)
		if err == nil {
			err = s.applyRegistryAndLinks(in.ThemeSlug, result)
		}
		if err != nil {
			errMsg := fmt.Sprintf("model generated a valid change, but writing it to the theme failed: %v", err)
			assistantMsg, recErr := s.chats.RecordAssistantMessage(ctx, c, "", chat.MessageStatusFailed, &errMsg, result.InputTokens, result.OutputTokens, chat.ApplyStatusNotApplicable)
			if recErr != nil {
				return GenerateOutcome{}, fmt.Errorf("record failed turn: %w (apply error: %w)", recErr, err)
			}
			return GenerateOutcome{Chat: c, UserMessage: userMsg, AssistantMessage: assistantMsg}, fmt.Errorf("apply to theme: %w", err)
		}
	}

	applyStatus := chat.ApplyStatusNotApplicable
	if hasChanges {
		applyStatus = chat.ApplyStatusApplied
	}

	assistantMsg, err := s.chats.RecordAssistantMessage(ctx, c, result.Summary, chat.MessageStatusCompleted, nil, result.InputTokens, result.OutputTokens, applyStatus)
	if err != nil {
		return GenerateOutcome{}, fmt.Errorf("record assistant message: %w", err)
	}

	files, err := s.persistFileRecords(ctx, c, assistantMsg.ID, written)
	if err != nil {
		return GenerateOutcome{}, fmt.Errorf("persist generated-file audit rows: %w", err)
	}

	return GenerateOutcome{Chat: c, UserMessage: userMsg, AssistantMessage: assistantMsg, Files: files}, nil
}

func (s *Service) buildThemeContext(themeSlug string) (ai.ThemeContext, error) {
	pagesJSON, err := s.store.ReadFile(themeSlug, pathPagesJSON)
	if err != nil {
		return ai.ThemeContext{}, err
	}
	defaultsJSON, err := s.store.ReadFile(themeSlug, "defaults.json")
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
func (s *Service) buildEditingFilesContext(ctx context.Context, themeSlug, chatID string) (map[string]string, error) {
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
		current, err := s.store.ReadFile(themeSlug, path)
		if err != nil {
			return nil, err
		}
		content := current
		if content == "" {
			content = latest[path].content
		}
		if len(content) > maxEditingFileBytes {
			content = content[:maxEditingFileBytes] + fmt.Sprintf("\n<!-- truncated: %d more bytes omitted -->", len(content)-maxEditingFileBytes)
		}
		editing[path] = content
	}
	return editing, nil
}

func toTurns(messages []chat.Message) []ai.Turn {
	turns := make([]ai.Turn, 0, len(messages))
	for _, m := range messages {
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
		if err := themefs.ValidateGeneratedFilePath(f.Path); err != nil {
			return fmt.Errorf("file %q: %w", f.Path, err)
		}
		if f.Action != "create" && f.Action != "update" {
			return fmt.Errorf("file %q: invalid action %q", f.Path, f.Action)
		}
	}
	for _, p := range r.LayoutLinksToAdd {
		if err := themefs.ValidateGeneratedFilePath(p); err != nil {
			return fmt.Errorf("layout css link %q: %w", p, err)
		}
	}
	for _, p := range r.LayoutScriptsToAdd {
		if err := themefs.ValidateGeneratedFilePath(p); err != nil {
			return fmt.Errorf("layout js link %q: %w", p, err)
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

// applyFilesToDisk re-validates and writes every proposed file to the real
// theme filesystem, stopping at the first failure — the caller is
// responsible for surfacing a partial-write error as a failed turn rather
// than silently losing which files did and didn't make it.
func (s *Service) applyFilesToDisk(themeSlug string, proposed []ai.GeneratedFile) ([]writtenFile, error) {
	written := make([]writtenFile, 0, len(proposed))
	for _, f := range proposed {
		if err := themefs.ValidateGeneratedFilePath(f.Path); err != nil {
			return written, fmt.Errorf("file %q failed re-validation before write: %w", f.Path, err)
		}
		previous, err := s.store.ReadFile(themeSlug, f.Path)
		if err != nil {
			return written, err
		}
		var previousPtr *string
		if previous != "" {
			previousPtr = &previous
		}
		if err := s.store.WriteFile(themeSlug, f.Path, f.Content); err != nil {
			return written, fmt.Errorf("write %q: %w", f.Path, err)
		}
		written = append(written, writtenFile{generated: f, previous: previousPtr})
	}
	return written, nil
}

// applyRegistryAndLinks performs the non-file side effects a proposal can
// include: merging a new pages.json entry, and splicing a new
// <link>/<script> tag into the layout files. These are structured
// merges/splices against files shared across the whole theme, never a
// plain overwrite (see THEME_ENGINE_SPEC.md).
func (s *Service) applyRegistryAndLinks(themeSlug string, result *ai.Result) error {
	if result.PageRegistryEntry != nil {
		entry := *result.PageRegistryEntry
		entry.PublishedAt = time.Now().UTC().Format(time.RFC3339)
		current, err := s.store.ReadFile(themeSlug, pathPagesJSON)
		if err != nil {
			return fmt.Errorf("register page: %w", err)
		}
		merged, err := themefs.MergePageRegistration([]byte(current), entry)
		if err != nil {
			return fmt.Errorf("register page: %w", err)
		}
		if err := s.store.WriteFile(themeSlug, pathPagesJSON, string(merged)); err != nil {
			return fmt.Errorf("register page: %w", err)
		}
	}
	for _, path := range result.LayoutLinksToAdd {
		if err := s.spliceLayoutLink(themeSlug, pathLayoutStart, path, themefs.AddStylesheetLink); err != nil {
			return fmt.Errorf("add layout css link %q: %w", path, err)
		}
	}
	for _, path := range result.LayoutScriptsToAdd {
		if err := s.spliceLayoutLink(themeSlug, pathLayoutEnd, path, themefs.AddDeferredScript); err != nil {
			return fmt.Errorf("add layout js link %q: %w", path, err)
		}
	}
	return nil
}

func (s *Service) spliceLayoutLink(themeSlug, layoutPath, linkPath string, splice func(string, string) (string, bool, error)) error {
	current, err := s.store.ReadFile(themeSlug, layoutPath)
	if err != nil {
		return err
	}
	updated, changed, err := splice(current, linkPath)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return s.store.WriteFile(themeSlug, layoutPath, updated)
}

// persistFileRecords writes the audit row for each file already written to
// disk (see applyFilesToDisk) — done after the assistant message exists
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

package themebuild

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"ai-chat/internal/themefs"
)

// ErrRevertBlockedByRunningGeneration means the chat has a generation
// currently in progress — reverting concurrently with it writing files
// would race, so the caller should ask the merchant to wait and retry.
var ErrRevertBlockedByRunningGeneration = errors.New("a generation is currently in progress for this chat")

// RevertResult summarizes what RevertToMessage changed.
type RevertResult struct {
	// RestoredFiles were rewritten back to the content they had immediately
	// after the target turn.
	RestoredFiles []string `json:"restored_files"`
	// DeletedFiles didn't exist yet as of the target turn (a later turn
	// created them) — reverting means removing them entirely.
	DeletedFiles []string `json:"deleted_files"`
}

// RevertToMessage restores every theme file this chat has ever generated
// back to the state it was in immediately after messageID's turn, undoing
// every later turn's file changes.
//
// This is ai-chat's answer to "git-backed drafts and revert": themefs.Store
// is an HTTP client to flowpos-backend's theme-file API (see its own doc
// comment) — this process never has the theme's files on local disk, so
// there is no working directory here to run `git init`/`git commit`
// against. The chat_generated_files audit trail this service already
// writes on every turn (content + previous_content, per file, per turn) is
// the equivalent history that actually exists in ai-chat's own boundary,
// so revert is built on that instead: for every file touched after the
// target turn, restore its last known content at-or-before that turn, or
// delete it if it didn't exist yet then.
//
// Content-only: a page's pages.json fields (title, SEO metadata, status)
// aren't tracked per-turn independently of file content, so those reflect
// the latest turn that set them even after a content revert — restoring
// them to a specific turn's values would need pages.json snapshotting this
// service doesn't do. Deleting a file does fully un-register its page,
// since flowpos-backend's own delete endpoint removes the pages.json entry
// too (see themefs.Store.DeleteFile).
func (s *Service) RevertToMessage(ctx context.Context, tenantID uint64, token, chatID, messageID string) (RevertResult, error) {
	if _, err := s.chats.GetChat(ctx, tenantID, chatID); err != nil {
		return RevertResult{}, err
	}
	target, err := s.chats.GetMessage(ctx, chatID, messageID)
	if err != nil {
		return RevertResult{}, err
	}

	if gen, err := s.repo.GetGeneration(ctx, chatID); err == nil && gen.Status == GenerationStatusRunning {
		return RevertResult{}, ErrRevertBlockedByRunningGeneration
	}

	files, err := s.repo.ListFilesByChat(ctx, chatID)
	if err != nil {
		return RevertResult{}, err
	}

	// latestAtOrBefore holds, per path, the last audit row at or before the
	// target turn — nil (absent) means the path didn't exist yet then.
	// touchedAfter holds every path any LATER turn touched, since only
	// those can possibly differ from the target turn's state.
	//
	// Ordering is by created_at, a DATETIME column with only second-level
	// precision (matching chat_messages/chat_generated_files as they exist
	// today) — two turns landing in the same wall-clock second would tie
	// here. In practice a turn takes seconds to minutes (it calls Claude),
	// so this isn't reachable through normal use; flagged rather than
	// silently assumed, since revert is exactly the kind of operation
	// where a wrong answer is worse than a slow one.
	latestAtOrBefore := make(map[string]GeneratedFile)
	touchedAfter := make(map[string]bool)
	for _, f := range files {
		if !f.CreatedAt.After(target.CreatedAt) {
			latestAtOrBefore[f.FilePath] = f
		} else {
			touchedAfter[f.FilePath] = true
		}
	}

	// Serialized against a concurrent generation the same way doGenerate's
	// own write section is (see themeLocks) — keyed by chatID rather than
	// themeSlug, since a chat carries no theme_slug of its own to lock on;
	// this still prevents two reverts of the same chat from racing.
	unlock := s.themeLocks.Lock(chatID)
	defer unlock()

	storeAuth := themefs.RequestAuth{Token: token, TenantID: tenantID}
	var result RevertResult
	for path := range touchedAfter {
		if before, ok := latestAtOrBefore[path]; ok {
			if err := s.store.WriteFile(ctx, storeAuth, path, before.Content, nil); err != nil {
				return result, fmt.Errorf("restore %q: %w", path, err)
			}
			result.RestoredFiles = append(result.RestoredFiles, path)
			continue
		}
		if err := s.store.DeleteFile(ctx, storeAuth, path); err != nil {
			return result, fmt.Errorf("delete %q: %w", path, err)
		}
		result.DeletedFiles = append(result.DeletedFiles, path)
	}

	sort.Strings(result.RestoredFiles)
	sort.Strings(result.DeletedFiles)
	return result, nil
}

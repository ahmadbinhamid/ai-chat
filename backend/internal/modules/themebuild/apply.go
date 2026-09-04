package themebuild

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"ai-chat/internal/themefs"

	"github.com/google/uuid"
)

var ErrApplyBlockedByRunningGeneration = errors.New("a generation is currently running or queued for this chat")

var ErrNoPendingChanges = errors.New("this chat has no pending changes to apply")

type ApplyResult struct {
	AppliedPaths []string `json:"applied_paths"`
}

// DiscardResult summarizes what DiscardDraft threw away.
type DiscardResult struct {
	DiscardedPaths []string `json:"discarded_paths"`
	DiscardedTurns int      `json:"discarded_turns"`
}

// DraftSummaryResult is GET /chat's pending_changes field — see
// Service.DraftSummary.
type DraftSummaryResult struct {
	HasChanges   bool     `json:"has_changes"`
	FilePaths    []string `json:"file_paths"`
	MessageCount int      `json:"message_count"`
}

func (s *Service) DraftSummary(ctx context.Context, chatID string) (DraftSummaryResult, error) {
	files, err := s.repo.PendingFiles(ctx, chatID)
	if err != nil {
		return DraftSummaryResult{}, err
	}
	if len(files) == 0 {
		return DraftSummaryResult{FilePaths: []string{}}, nil
	}

	seenPaths := make(map[string]bool)
	seenMessages := make(map[string]bool)
	var paths []string
	for _, f := range files {
		seenMessages[f.MessageID] = true
		if f.Kind == GeneratedFileKindLayout {
			continue
		}
		if seenPaths[f.FilePath] {
			continue
		}
		seenPaths[f.FilePath] = true
		paths = append(paths, f.FilePath)
	}
	if paths == nil {
		paths = []string{}
	}
	return DraftSummaryResult{HasChanges: true, FilePaths: paths, MessageCount: len(seenMessages)}, nil
}

func (s *Service) ApplyDraft(ctx context.Context, tenantID uint64, token, chatID, themeSlug string) (ApplyResult, error) {
	if _, err := s.chats.GetChat(ctx, tenantID, chatID); err != nil {
		return ApplyResult{}, err
	}

	pending, err := s.repo.ListPending(ctx, chatID)
	if err != nil {
		return ApplyResult{}, err
	}
	if len(pending) > 0 {
		return ApplyResult{}, ErrApplyBlockedByRunningGeneration
	}

	files, err := s.repo.PendingFiles(ctx, chatID)
	if err != nil {
		return ApplyResult{}, err
	}
	if len(files) == 0 {
		return ApplyResult{}, ErrNoPendingChanges
	}

	unlock, err := s.themeLocks.Lock(ctx, themeLockKey(tenantID, themeSlug))
	if err != nil {
		return ApplyResult{}, fmt.Errorf("apply draft: %w", err)
	}
	defer unlock()

	plan := pendingFilesToPlan(files)

	storeAuth := themefs.RequestAuth{Token: token, TenantID: tenantID}
	written, err := s.commitWritePlan(ctx, storeAuth, plan)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("apply draft: %w", err)
	}

	if err := s.repo.MarkMessagesApplied(ctx, chatID, time.Now().UTC()); err != nil {
		return ApplyResult{}, fmt.Errorf("mark draft applied: %w", err)
	}

	paths := make([]string, 0, len(written))
	for _, w := range written {
		paths = append(paths, w.generated.Path)
	}
	return ApplyResult{AppliedPaths: paths}, nil
}

func (s *Service) DraftFiles(ctx context.Context, tenantID uint64, token, chatID string) (map[string]string, error) {
	if _, err := s.chats.GetChat(ctx, tenantID, chatID); err != nil {
		return nil, err
	}
	draft, err := s.repo.DraftFiles(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("load draft overlay: %w", err)
	}
	store := themefs.NewOverlayStore(s.store, draft)
	storeAuth := themefs.RequestAuth{Token: token, TenantID: tenantID}
	return s.LoadThemeFiles(ctx, store, storeAuth, true)
}

func pendingFilesToPlan(files []GeneratedFile) writePlan {
	var plan writePlan
	proposedByPath := make(map[string]int) // path -> index into plan.files

	for _, f := range files {
		switch f.Kind {
		case GeneratedFileKindLayout:
			pf := &planFile{path: f.FilePath, action: f.Action, content: f.Content}
			switch f.FilePath {
			case pathLayoutStart:
				plan.layoutStart = pf
			case pathLayoutEnd:
				plan.layoutEnd = pf
			}
		default:
			pf := planFile{path: f.FilePath, action: f.Action, content: f.Content, previous: f.PreviousContent, pageMeta: f.PageMeta}
			if idx, ok := proposedByPath[f.FilePath]; ok {
				if pf.pageMeta == nil {
					pf.pageMeta = plan.files[idx].pageMeta
				}
				if plan.files[idx].action == FileActionCreate {
					pf.action = FileActionCreate
				}
				plan.files[idx] = pf
			} else {
				proposedByPath[f.FilePath] = len(plan.files)
				plan.files = append(plan.files, pf)
			}
		}
	}
	return plan
}

func (s *Service) DiscardDraft(ctx context.Context, tenantID uint64, chatID string) (DiscardResult, error) {
	if _, err := s.chats.GetChat(ctx, tenantID, chatID); err != nil {
		return DiscardResult{}, err
	}

	pending, err := s.repo.ListPending(ctx, chatID)
	if err != nil {
		return DiscardResult{}, err
	}
	if len(pending) > 0 {
		return DiscardResult{}, ErrApplyBlockedByRunningGeneration
	}

	files, err := s.repo.PendingFiles(ctx, chatID)
	if err != nil {
		return DiscardResult{}, err
	}
	if len(files) == 0 {
		return DiscardResult{}, ErrNoPendingChanges
	}

	if err := s.repo.MarkMessagesDiscarded(ctx, chatID); err != nil {
		return DiscardResult{}, err
	}

	seenPaths := make(map[string]bool)
	seenMessages := make(map[string]bool)
	var paths []string
	for _, f := range files {
		seenMessages[f.MessageID] = true
		if !seenPaths[f.FilePath] {
			seenPaths[f.FilePath] = true
			paths = append(paths, f.FilePath)
		}
	}
	return DiscardResult{DiscardedPaths: paths, DiscardedTurns: len(seenMessages)}, nil
}

var ErrManualEditFileNotFound = errors.New("this file does not exist in the current draft")

func (s *Service) SaveManualEdit(ctx context.Context, tenantID uint64, token, chatID, filePath, content string) (GeneratedFile, error) {
	if err := themefs.ValidatePathSafety(filePath); err != nil {
		return GeneratedFile{}, err
	}

	c, err := s.chats.GetChat(ctx, tenantID, chatID)
	if err != nil {
		return GeneratedFile{}, err
	}

	var previous string
	if isEditableImagePath(filePath) {
		storeAuth := themefs.RequestAuth{Token: token, TenantID: tenantID}
		bytes, err := s.ReadThemeAssetBytes(ctx, storeAuth, filePath)
		if err != nil {
			return GeneratedFile{}, err
		}
		if len(bytes) == 0 {
			return GeneratedFile{}, fmt.Errorf("%w: %s", ErrManualEditFileNotFound, filePath)
		}
		previous = base64.StdEncoding.EncodeToString(bytes)
	} else {
		current, err := s.DraftFiles(ctx, tenantID, token, chatID)
		if err != nil {
			return GeneratedFile{}, err
		}
		var existed bool
		previous, existed = current[filePath]
		if !existed {
			return GeneratedFile{}, fmt.Errorf("%w: %s", ErrManualEditFileNotFound, filePath)
		}
	}

	msg, err := s.chats.RecordManualEditMessage(ctx, c, filePath)
	if err != nil {
		return GeneratedFile{}, err
	}

	now := time.Now().UTC()
	f := GeneratedFile{
		ID:              uuid.NewString(),
		MessageID:       msg.ID,
		ChatID:          c.ID,
		FilePath:        filePath,
		Action:          FileActionUpdate,
		Kind:            GeneratedFileKindProposed,
		Language:        languageFor(filePath),
		Content:         content,
		PreviousContent: &previous,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.repo.CreateFile(ctx, f); err != nil {
		return GeneratedFile{}, err
	}
	return f, nil
}

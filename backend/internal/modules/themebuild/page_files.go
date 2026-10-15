package themebuild

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"ai-chat/internal/liquiddeps"
	"ai-chat/internal/safego"
	"ai-chat/internal/themefs"
)

// PageFiles is what one page renders from. Complete means it is the whole of LoadThemeFiles (with assets): a
// reference couldn't be resolved statically, so every file was loaded.
type PageFiles struct {
	Files    map[string]string `json:"files"`
	Complete bool              `json:"complete"`
}

// ErrInvalidEntry is an entry path that isn't a safe theme-relative .liquid file.
var ErrInvalidEntry = errors.New("entry must be a theme-relative .liquid path")

// DraftPageFiles is DraftFiles narrowed to the files entry renders, plus pages.json.
func (s *Service) DraftPageFiles(ctx context.Context, tenantID uint64, token, chatID, entry string) (PageFiles, error) {
	if !strings.HasSuffix(entry, ".liquid") || themefs.ValidatePathSafety(entry) != nil {
		return PageFiles{}, ErrInvalidEntry
	}
	if _, err := s.chats.GetChat(ctx, tenantID, chatID); err != nil {
		return PageFiles{}, err
	}
	draft, err := s.repo.DraftFiles(ctx, chatID)
	if err != nil {
		return PageFiles{}, fmt.Errorf("load draft overlay: %w", err)
	}
	store := themefs.NewOverlayStore(s.store, draft)
	return LoadPageFiles(ctx, store, themefs.RequestAuth{Token: token, TenantID: tenantID}, entry)
}

// LoadPageFiles reads entry and everything it pulls in, starting each read as soon as its file is discovered rather
// than level by level. A missing entry yields just pages.json, so the caller can resolve its route and ask again.
func LoadPageFiles(ctx context.Context, store themefs.ThemeStore, storeAuth themefs.RequestAuth, entry string) (PageFiles, error) {
	tree, err := store.ListFiles(ctx, storeAuth)
	if err != nil {
		return PageFiles{}, fmt.Errorf("list theme files: %w", err)
	}
	paths := make(map[string]bool)
	flattenFileTree(tree, paths)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		files    = map[string]string{}
		seen     = map[string]bool{}
		dynamic  bool
		firstErr error
	)
	sem := make(chan struct{}, loadThemeFilesConcurrency)

	var visit func(path string)
	visit = func(path string) {
		mu.Lock()
		if seen[path] || !paths[path] || !isRenderFile(path, true) {
			mu.Unlock()
			return
		}
		seen[path] = true
		mu.Unlock()

		// Added before the parent's Done, so the count can't reach zero while children are still being queued.
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer safego.Recover("themebuild.loadPageFile")
			fail := func(err error) {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("read %s: %w", path, err)
					cancel()
				}
				mu.Unlock()
			}
			// A panicking read must fail the load, not return a page with a file silently missing.
			finished := false
			defer func() {
				if !finished {
					fail(errors.New("read did not finish"))
				}
			}()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				finished = true
				return
			}
			content, err := func() (string, error) {
				defer func() { <-sem }()
				return store.ReadFile(ctx, storeAuth, path)
			}()
			if err != nil {
				fail(err)
				return
			}
			var refs []string
			isDynamic := false
			if strings.HasSuffix(path, ".liquid") {
				refs, isDynamic = liquiddeps.References(content)
			}
			mu.Lock()
			files[path] = content
			dynamic = dynamic || isDynamic
			mu.Unlock()
			for _, ref := range refs {
				visit(ref)
			}
			finished = true
		}()
	}
	visit(pathPagesJSON)
	visit(entry)
	wg.Wait()

	if firstErr != nil {
		return PageFiles{}, firstErr
	}
	// Reads skipped on cancellation leave no error behind, so a cancelled load must not pass as a whole page.
	if err := ctx.Err(); err != nil {
		return PageFiles{}, err
	}
	if !dynamic {
		return PageFiles{Files: files}, nil
	}

	var rest []string
	for path := range paths {
		if _, done := files[path]; !done && isRenderFile(path, true) {
			rest = append(rest, path)
		}
	}
	more, err := readThemeFiles(ctx, store, storeAuth, rest)
	if err != nil {
		return PageFiles{}, err
	}
	for path, content := range more {
		files[path] = content
	}
	return PageFiles{Files: files, Complete: true}, nil
}

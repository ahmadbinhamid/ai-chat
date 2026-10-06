package themebuild

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"ai-chat/internal/themefs"
)

// countingStore is an in-memory ThemeStore recording how often each read reaches it.
type countingStore struct {
	mu        sync.Mutex
	files     map[string]string
	reads     int
	listCalls int
}

func (s *countingStore) ReadFile(_ context.Context, _ themefs.RequestAuth, relPath string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	return s.files[relPath], nil
}

func (s *countingStore) ListFiles(context.Context, themefs.RequestAuth) ([]themefs.FileTreeEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listCalls++
	entries := make([]themefs.FileTreeEntry, 0, len(s.files))
	for p := range s.files {
		entries = append(entries, themefs.FileTreeEntry{Name: p, Path: p, Type: "file"})
	}
	return entries, nil
}

func (s *countingStore) WriteFile(context.Context, themefs.RequestAuth, string, string, *themefs.PageMeta) error {
	return nil
}

func (s *countingStore) DeleteFile(context.Context, themefs.RequestAuth, string) error { return nil }
func (s *countingStore) UploadFile(context.Context, themefs.RequestAuth, string, []byte, string) error {
	return nil
}

func (s *countingStore) counts() (reads, lists int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads, s.listCalls
}

func grepThemeFiles() map[string]string {
	return map[string]string{
		"pages/home.liquid":        "hero title\nTODO footer",
		"components/header.liquid": "logo\nnav",
		"components/css/hero.css":  ".hero { color: red; }",
		"images/logo.png":          "binary-ish TODO",
	}
}

// grep_theme reads through the generation's CachingStore, so later greps reuse fetched content instead of re-reading it.
func TestGrepTheme_SecondGrepInGenerationReusesCachedReads(t *testing.T) {
	base := &countingStore{files: grepThemeFiles()}
	exec := (&Service{}).buildToolExecutor(themefs.NewCachingStore(base), testStoreAuth())

	grep := func(pattern, glob string) string {
		t.Helper()
		input, _ := json.Marshal(grepThemeInput{Pattern: pattern, PathGlob: glob})
		out, err := exec(context.Background(), "grep_theme", input)
		if err != nil {
			t.Fatalf("grep %q: %v", pattern, err)
		}
		return out
	}

	first := grep("TODO", "")
	readsAfterFirst, listsAfterFirst := base.counts()
	second := grep("color|logo", "")
	third := grep("hero", "pages/*.liquid")
	reads, lists := base.counts()

	if readsAfterFirst != 3 || listsAfterFirst != 1 {
		t.Fatalf("first grep: expected 3 searchable reads + 1 list, got %d reads, %d lists", readsAfterFirst, listsAfterFirst)
	}
	if reads != readsAfterFirst || lists != listsAfterFirst {
		t.Errorf("expected later greps served from cache, reads %d→%d, lists %d→%d", readsAfterFirst, reads, listsAfterFirst, lists)
	}

	// Search behaviour is unchanged: extension filter, alternation, glob.
	if !strings.Contains(first, "pages/home.liquid:2: TODO footer") || strings.Contains(first, "images/logo.png") {
		t.Errorf("first grep: unexpected output %q", first)
	}
	if !strings.Contains(second, "components/css/hero.css:1:") || !strings.Contains(second, "components/header.liquid:1: logo") {
		t.Errorf("second grep: unexpected output %q", second)
	}
	if !strings.Contains(third, "pages/home.liquid:1: hero title") || strings.Contains(third, "hero.css") {
		t.Errorf("third grep: glob not applied, got %q", third)
	}
}

// Nothing is pre-built for grep: a generation that never greps reads nothing on grep's behalf.
func TestGrepTheme_NoGrepNoReads(t *testing.T) {
	base := &countingStore{files: grepThemeFiles()}
	_ = (&Service{}).buildToolExecutor(themefs.NewCachingStore(base), testStoreAuth())
	if reads, lists := base.counts(); reads != 0 || lists != 0 {
		t.Errorf("expected no store access without a grep, got %d reads, %d lists", reads, lists)
	}
}

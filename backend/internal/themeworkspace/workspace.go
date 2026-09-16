// Package themeworkspace keeps a CPU-local on-disk mirror of a tenant theme
// for search/read during generation. DeepSeek remains the only model; this
// package does no inference — filesystem, hashing, optional ripgrep, and
// FlowPOS sync of changed files only.
package themeworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"ai-chat/internal/themefs"

	"golang.org/x/sync/errgroup"
)

const (
	metaFileName    = ".workspace-meta.json"
	filesDirName    = "files"
	syncConcurrency = 8
	// syncFreshnessTTL: FlowPOS has no theme revision field we can trust.
	// Within this window, a populated local mirror is reused without ListFiles
	// or ReadFile. After the TTL, EnsureSynced re-lists and only fetches
	// missing/changed text paths (hashes still skip unchanged files).
	syncFreshnessTTL = 15 * time.Minute
)

// textThemeExt are source files useful for AI edit/search. Binary assets are
// listed in the tree but never ReadFile'd as text — FlowPOS returns 422 for
// many image paths (e.g. .avif), and a single failure used to abort the
// whole workspace sync.
var textThemeExt = map[string]bool{
	".liquid": true,
	".css":    true,
	".scss":   true,
	".sass":   true,
	".js":     true,
	".ts":     true,
	".jsx":    true,
	".tsx":    true,
	".json":   true,
	".yaml":   true,
	".yml":    true,
	".md":     true,
	".txt":    true,
	".svg":    true, // markup, not a raster binary
	".map":    true,
}

// IsAITextPath reports whether relPath should be synced/read as theme text.
func IsAITextPath(relPath string) bool {
	ext := strings.ToLower(path.Ext(relPath))
	if textThemeExt[ext] {
		return true
	}
	// Extensionless theme-root files used by the engine.
	base := path.Base(relPath)
	return base == "robots.txt" || base == "defaults.json" || base == "pages.json"
}

// Manager owns the workspace root directory (one process-wide root, many
// tenant/theme subdirs).
type Manager struct {
	root string
}

// NewManager returns a Manager rooted at root. Empty root disables local-first.
func NewManager(root string) *Manager {
	if strings.TrimSpace(root) == "" {
		return nil
	}
	return &Manager{root: root}
}

// Enabled reports whether local-first disk caching is configured.
func (m *Manager) Enabled() bool {
	return m != nil && m.root != ""
}

// Workspace is one tenant+theme on-disk mirror.
type Workspace struct {
	manager  *Manager
	tenantID uint64
	slug     string
	dir      string
	filesDir string

	mu       sync.Mutex
	meta     workspaceMeta
	dirty    map[string]struct{} // local writes not yet pushed to FlowPOS
	remote   themefs.ThemeStore  // authoritative remote for miss / sync
}

type workspaceMeta struct {
	ThemeSlug string            `json:"theme_slug"`
	TenantID  uint64            `json:"tenant_id"`
	SyncedAt  time.Time         `json:"synced_at"`
	Hashes    map[string]string `json:"hashes"` // relPath → sha256 hex
}

// Open returns (and creates) the on-disk workspace for tenant/slug backed by remote.
func (m *Manager) Open(tenantID uint64, slug string, remote themefs.ThemeStore) (*Workspace, error) {
	if !m.Enabled() {
		return nil, fmt.Errorf("theme workspace disabled")
	}
	if err := themefs.ValidateThemeSlug(slug); err != nil {
		return nil, err
	}
	dir := filepath.Join(m.root, fmt.Sprintf("%d", tenantID), slug)
	filesDir := filepath.Join(dir, filesDirName)
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace: %w", err)
	}
	ws := &Workspace{
		manager:  m,
		tenantID: tenantID,
		slug:     slug,
		dir:      dir,
		filesDir: filesDir,
		dirty:    map[string]struct{}{},
		remote:   remote,
		meta: workspaceMeta{
			ThemeSlug: slug,
			TenantID:  tenantID,
			Hashes:    map[string]string{},
		},
	}
	_ = ws.loadMeta()
	return ws, nil
}

func (w *Workspace) loadMeta() error {
	raw, err := os.ReadFile(filepath.Join(w.dir, metaFileName))
	if err != nil {
		return err
	}
	var meta workspaceMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return err
	}
	if meta.Hashes == nil {
		meta.Hashes = map[string]string{}
	}
	w.meta = meta
	return nil
}

func (w *Workspace) saveMeta() error {
	w.meta.SyncedAt = time.Now().UTC()
	raw, err := json.MarshalIndent(w.meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(w.dir, metaFileName), raw, 0o644)
}

func hashContent(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// SyncStats is returned by EnsureSynced for structured timing logs.
type SyncStats struct {
	Listed     int
	Fetched    int
	Skipped    int
	SkippedBin int
	FetchErrs  int
	ElapsedMs  int64
}

// EnsureSynced pulls missing/changed text theme files from FlowPOS into the
// local mirror. Binary/unsupported assets are skipped (not fatal). Individual
// ReadFile failures are logged and skipped so one bad path cannot disable
// the whole local-first path.
//
// When the mirror was synced within syncFreshnessTTL and local files still
// match recorded hashes, this is a no-op (no FlowPOS List/Read) so subsequent
// generations reuse the disk workspace.
func (w *Workspace) EnsureSynced(ctx context.Context, auth themefs.RequestAuth) (stats SyncStats, err error) {
	start := time.Now()

	w.mu.Lock()
	syncedAt := w.meta.SyncedAt
	fresh := !syncedAt.IsZero() &&
		time.Since(syncedAt) < syncFreshnessTTL &&
		len(w.meta.Hashes) > 0
	hashSnapshot := make(map[string]string, len(w.meta.Hashes))
	for k, v := range w.meta.Hashes {
		hashSnapshot[k] = v
	}
	w.mu.Unlock()

	if fresh && w.localMirrorIntact(hashSnapshot) {
		stats = SyncStats{
			Listed:    len(hashSnapshot),
			Skipped:   len(hashSnapshot),
			ElapsedMs: time.Since(start).Milliseconds(),
		}
		slog.Info("themeworkspace: reuse local mirror (fresh)",
			"tenant_id", w.tenantID, "theme_slug", w.slug,
			"files", len(hashSnapshot), "synced_at", syncedAt,
			"elapsed_ms", stats.ElapsedMs)
		return stats, nil
	}

	tree, err := w.remote.ListFiles(ctx, auth)
	if err != nil {
		return stats, fmt.Errorf("list remote theme: %w", err)
	}
	paths := map[string]bool{}
	flatten(tree, paths)
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)

	w.mu.Lock()
	existing := make(map[string]string, len(w.meta.Hashes))
	for k, v := range w.meta.Hashes {
		existing[k] = v
	}
	w.mu.Unlock()

	var (
		mu         sync.Mutex
		fetchedN   int
		skipped    int
		skippedBin int
		fetchErrs  int
	)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(syncConcurrency)
	for _, p := range sorted {
		p := p
		g.Go(func() error {
			if !IsAITextPath(p) {
				mu.Lock()
				skippedBin++
				mu.Unlock()
				return nil
			}
			localPath := w.absPath(p)
			// Fast path: file on disk with recorded hash — skip remote read.
			if _, err := os.Stat(localPath); err == nil {
				if _, ok := existing[p]; ok {
					mu.Lock()
					skipped++
					mu.Unlock()
					return nil
				}
			}
			content, err := w.remote.ReadFile(gctx, auth, p)
			if err != nil {
				mu.Lock()
				fetchErrs++
				mu.Unlock()
				slog.Warn("themeworkspace: skip remote read",
					"path", p, "error", err.Error())
				return nil // non-fatal — continue syncing other files
			}
			h := hashContent(content)
			if err := w.writeLocalFile(p, content); err != nil {
				mu.Lock()
				fetchErrs++
				mu.Unlock()
				slog.Warn("themeworkspace: skip local write",
					"path", p, "error", err.Error())
				return nil
			}
			mu.Lock()
			fetchedN++
			w.mu.Lock()
			w.meta.Hashes[p] = h
			w.mu.Unlock()
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return stats, err
	}
	// Drop hashes for paths that no longer exist remotely.
	w.mu.Lock()
	for p := range w.meta.Hashes {
		if !paths[p] {
			delete(w.meta.Hashes, p)
			_ = os.Remove(w.absPath(p))
		}
	}
	err = w.saveMeta()
	w.mu.Unlock()
	stats = SyncStats{
		Listed:     len(sorted),
		Fetched:    fetchedN,
		Skipped:    skipped,
		SkippedBin: skippedBin,
		FetchErrs:  fetchErrs,
		ElapsedMs:  time.Since(start).Milliseconds(),
	}
	slog.Info("themeworkspace: sync finished",
		"tenant_id", w.tenantID, "theme_slug", w.slug,
		"listed", stats.Listed, "fetched", stats.Fetched, "skipped", stats.Skipped,
		"skipped_binary", stats.SkippedBin, "fetch_errors", stats.FetchErrs,
		"elapsed_ms", stats.ElapsedMs)
	return stats, err
}

// localMirrorIntact reports whether every hashed text file still exists on disk.
func (w *Workspace) localMirrorIntact(hashes map[string]string) bool {
	if len(hashes) == 0 {
		return false
	}
	for p := range hashes {
		if _, err := os.Stat(w.absPath(p)); err != nil {
			return false
		}
	}
	return true
}

func (w *Workspace) absPath(rel string) string {
	return filepath.Join(w.filesDir, filepath.FromSlash(rel))
}

func (w *Workspace) writeLocalFile(rel, content string) error {
	abs := w.absPath(rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, []byte(content), 0o644)
}

// ReadFile implements local-first read (disk, then remote fill).
func (w *Workspace) ReadFile(ctx context.Context, auth themefs.RequestAuth, relPath string) (string, error) {
	abs := w.absPath(relPath)
	if raw, err := os.ReadFile(abs); err == nil {
		return string(raw), nil
	}
	content, err := w.remote.ReadFile(ctx, auth, relPath)
	if err != nil {
		return "", err
	}
	_ = w.writeLocalFile(relPath, content)
	w.mu.Lock()
	w.meta.Hashes[relPath] = hashContent(content)
	_ = w.saveMeta()
	w.mu.Unlock()
	return content, nil
}

// WriteFile updates the local mirror and marks the path dirty for a later
// PushChanged sync. Remote write is deferred to PushChanged / Apply.
func (w *Workspace) WriteFile(ctx context.Context, auth themefs.RequestAuth, relPath, content string, meta *themefs.PageMeta) error {
	if err := w.writeLocalFile(relPath, content); err != nil {
		return err
	}
	w.mu.Lock()
	w.meta.Hashes[relPath] = hashContent(content)
	w.dirty[relPath] = struct{}{}
	_ = w.saveMeta()
	w.mu.Unlock()
	// Also write through to remote immediately when this workspace is used
	// as the Apply path store — callers that want local-only staging use
	// OverlayStore on top and never call WriteFile here until Apply.
	return w.remote.WriteFile(ctx, auth, relPath, content, meta)
}

// DeleteFile removes locally and remotely.
func (w *Workspace) DeleteFile(ctx context.Context, auth themefs.RequestAuth, relPath string) error {
	_ = os.Remove(w.absPath(relPath))
	w.mu.Lock()
	delete(w.meta.Hashes, relPath)
	delete(w.dirty, relPath)
	_ = w.saveMeta()
	w.mu.Unlock()
	return w.remote.DeleteFile(ctx, auth, relPath)
}

// ListFiles prefers the local tree when the workspace has been synced;
// falls back to remote.
func (w *Workspace) ListFiles(ctx context.Context, auth themefs.RequestAuth) ([]themefs.FileTreeEntry, error) {
	w.mu.Lock()
	n := len(w.meta.Hashes)
	w.mu.Unlock()
	if n == 0 {
		return w.remote.ListFiles(ctx, auth)
	}
	var entries []themefs.FileTreeEntry
	err := filepath.WalkDir(w.filesDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(w.filesDir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		entries = append(entries, themefs.FileTreeEntry{Name: path.Base(rel), Path: rel, Type: "file"})
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Build a flat tree FlowPOS-style (top-level list of files) — themebuild
	// flattenFileTree already handles nested Children; a flat list of files
	// with Path set is enough.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

// Grep searches the local workspace. Uses ripgrep when available, otherwise
// a pure-Go walk (same RE2 semantics as themebuild.execGrepTheme).
func (w *Workspace) Grep(ctx context.Context, pattern, pathGlob string, maxMatches int) (string, error) {
	start := time.Now()
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern: %w", err)
	}
	if maxMatches <= 0 {
		maxMatches = 200
	}
	if out, ok := w.grepRipgrep(ctx, pattern, pathGlob, maxMatches); ok {
		slog.Info("themeworkspace: grep", "engine", "ripgrep", "elapsed_ms", time.Since(start).Milliseconds())
		return out, nil
	}
	out, err := w.grepGo(ctx, re, pathGlob, maxMatches)
	slog.Info("themeworkspace: grep", "engine", "go", "elapsed_ms", time.Since(start).Milliseconds())
	return out, err
}

func (w *Workspace) grepRipgrep(ctx context.Context, pattern, pathGlob string, maxMatches int) (string, bool) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		return "", false
	}
	args := []string{"--line-number", "--no-heading", "--color", "never", "-e", pattern}
	if pathGlob != "" {
		args = append(args, "--glob", pathGlob)
	}
	args = append(args, w.filesDir)
	cmd := exec.CommandContext(ctx, rg, args...)
	raw, err := cmd.Output()
	if err != nil {
		// Exit 1 = no matches
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return "(no matches)", true
		}
		return "", false
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	var b strings.Builder
	n := 0
	for _, line := range lines {
		if line == "" {
			continue
		}
		// Convert abs path to theme-relative.
		if idx := strings.Index(line, w.filesDir); idx >= 0 {
			rest := line[idx+len(w.filesDir):]
			rest = strings.TrimPrefix(rest, string(filepath.Separator))
			rest = filepath.ToSlash(rest)
			// rest is "path:line:text" after first colon from rg... actually
			// format is /abs/path:line:text — rebuild from rel.
			parts := strings.SplitN(rest, ":", 3)
			if len(parts) == 3 {
				fmt.Fprintf(&b, "%s:%s: %s\n", parts[0], parts[1], strings.TrimSpace(parts[2]))
				n++
				if n >= maxMatches {
					fmt.Fprintf(&b, "(stopped at %d matches)\n", maxMatches)
					break
				}
				continue
			}
		}
		b.WriteString(line)
		b.WriteByte('\n')
		n++
		if n >= maxMatches {
			break
		}
	}
	if n == 0 {
		return "(no matches)", true
	}
	return b.String(), true
}

func (w *Workspace) grepGo(ctx context.Context, re *regexp.Regexp, pathGlob string, maxMatches int) (string, error) {
	var b strings.Builder
	matches := 0
	err := filepath.WalkDir(w.filesDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		rel, err := filepath.Rel(w.filesDir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		ext := path.Ext(rel)
		switch ext {
		case ".liquid", ".css", ".js", ".json":
		default:
			return nil
		}
		if pathGlob != "" {
			ok, globErr := path.Match(pathGlob, rel)
			if globErr != nil || !ok {
				return nil
			}
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if re.MatchString(line) {
				fmt.Fprintf(&b, "%s:%d: %s\n", rel, i+1, strings.TrimSpace(line))
				matches++
				if matches >= maxMatches {
					fmt.Fprintf(&b, "(stopped at %d matches)\n", maxMatches)
					return fs.SkipAll
				}
			}
		}
		return nil
	})
	if err != nil && err != fs.SkipAll {
		return "", err
	}
	if matches == 0 {
		return "(no matches)", nil
	}
	return b.String(), nil
}

// PushChanged writes dirty local paths to FlowPOS (changed files only).
func (w *Workspace) PushChanged(ctx context.Context, auth themefs.RequestAuth) (pushed int, err error) {
	w.mu.Lock()
	paths := make([]string, 0, len(w.dirty))
	for p := range w.dirty {
		paths = append(paths, p)
	}
	w.mu.Unlock()
	sort.Strings(paths)
	for _, p := range paths {
		raw, err := os.ReadFile(w.absPath(p))
		if err != nil {
			return pushed, err
		}
		if err := w.remote.WriteFile(ctx, auth, p, string(raw), nil); err != nil {
			return pushed, fmt.Errorf("push %s: %w", p, err)
		}
		w.mu.Lock()
		delete(w.dirty, p)
		w.mu.Unlock()
		pushed++
	}
	return pushed, nil
}

// Store adapts Workspace to themefs.ThemeStore for generation tooling.
type Store struct {
	*Workspace
}

func flatten(entries []themefs.FileTreeEntry, out map[string]bool) {
	for _, e := range entries {
		if e.Type == "directory" || len(e.Children) > 0 {
			flatten(e.Children, out)
			continue
		}
		p := e.Path
		if p == "" {
			p = e.Name
		}
		if p != "" {
			out[p] = true
		}
	}
}

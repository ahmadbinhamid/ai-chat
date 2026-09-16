package themeworkspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"ai-chat/internal/themefs"
)

type memStore struct {
	files map[string]string
}

func (m *memStore) ReadFile(_ context.Context, _ themefs.RequestAuth, relPath string) (string, error) {
	return m.files[relPath], nil
}
func (m *memStore) WriteFile(_ context.Context, _ themefs.RequestAuth, relPath, content string, _ *themefs.PageMeta) error {
	if m.files == nil {
		m.files = map[string]string{}
	}
	m.files[relPath] = content
	return nil
}
func (m *memStore) DeleteFile(_ context.Context, _ themefs.RequestAuth, relPath string) error {
	delete(m.files, relPath)
	return nil
}
func (m *memStore) ListFiles(context.Context, themefs.RequestAuth) ([]themefs.FileTreeEntry, error) {
	var out []themefs.FileTreeEntry
	for p := range m.files {
		out = append(out, themefs.FileTreeEntry{Name: filepath.Base(p), Path: p, Type: "file"})
	}
	return out, nil
}

func TestWorkspace_SyncReadGrepLocal(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root)
	remote := &memStore{files: map[string]string{
		"components/header.liquid": "<header>\n  <nav>Home</nav>\n</header>\n",
		"pages/home.liquid":        "{% render 'layout-start' %}\n",
	}}
	ws, err := mgr.Open(1, "demo-theme", remote)
	if err != nil {
		t.Fatal(err)
	}
	auth := themefs.RequestAuth{Token: "t", TenantID: 1}
	stats, err := ws.EnsureSynced(context.Background(), auth)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Fetched != 2 {
		t.Fatalf("fetched=%d want 2", stats.Fetched)
	}
	// Second sync should skip remote reads.
	stats2, err := ws.EnsureSynced(context.Background(), auth)
	if err != nil {
		t.Fatal(err)
	}
	if stats2.Fetched != 0 || stats2.Skipped != 2 {
		t.Fatalf("second sync fetched=%d skipped=%d", stats2.Fetched, stats2.Skipped)
	}
	got, err := ws.ReadFile(context.Background(), auth, "components/header.liquid")
	if err != nil || !contains(got, "<nav>Home</nav>") {
		t.Fatalf("read=%q err=%v", got, err)
	}
	out, err := ws.Grep(context.Background(), "nav", "components/*", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(out, "components/header.liquid") {
		t.Fatalf("grep output missing path: %q", out)
	}
	if _, err := os.Stat(filepath.Join(root, "1", "demo-theme", "files", "components", "header.liquid")); err != nil {
		t.Fatalf("expected on-disk file: %v", err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		})())
}

func TestWorkspace_SkipsBinaryAssets(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root)
	remote := &memStore{files: map[string]string{
		"components/header.liquid": "<header></header>\n",
		"images/hero.avif":         "not-real-avif-bytes",
		"images/logo.png":          "png",
	}}
	ws, err := mgr.Open(1, "demo-theme", remote)
	if err != nil {
		t.Fatal(err)
	}
	auth := themefs.RequestAuth{Token: "t", TenantID: 1}
	stats, err := ws.EnsureSynced(context.Background(), auth)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Fetched != 1 {
		t.Fatalf("fetched=%d want 1 (liquid only)", stats.Fetched)
	}
	if stats.SkippedBin < 2 {
		t.Fatalf("skipped_binary=%d want >=2", stats.SkippedBin)
	}
	if !IsAITextPath("components/header.liquid") || IsAITextPath("images/hero.avif") {
		t.Fatal("IsAITextPath misclassified")
	}
}

func TestWorkspace_SyncContinuesWhenOneReadFails(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root)
	remote := &failOneStore{
		memStore: memStore{files: map[string]string{
			"pages/home.liquid":        "home",
			"components/header.liquid": "header",
		}},
		failPath: "components/header.liquid",
	}
	ws, err := mgr.Open(2, "demo-theme", remote)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := ws.EnsureSynced(context.Background(), themefs.RequestAuth{})
	if err != nil {
		t.Fatalf("sync must not fail hard: %v", err)
	}
	if stats.Fetched != 1 || stats.FetchErrs != 1 {
		t.Fatalf("fetched=%d fetch_errors=%d", stats.Fetched, stats.FetchErrs)
	}
}

type failOneStore struct {
	memStore
	failPath string
}

func (f *failOneStore) ReadFile(ctx context.Context, auth themefs.RequestAuth, relPath string) (string, error) {
	if relPath == f.failPath {
		return "", fmt.Errorf("unexpected status 422: path invalid")
	}
	return f.memStore.ReadFile(ctx, auth, relPath)
}

func TestWorkspace_ReusesFreshMirrorWithoutRemote(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root)
	remote := &countingListStore{memStore: memStore{files: map[string]string{
		"components/header.liquid": "<header></header>\n",
		"pages/home.liquid":        "home\n",
	}}}
	ws, err := mgr.Open(3, "demo-theme", remote)
	if err != nil {
		t.Fatal(err)
	}
	auth := themefs.RequestAuth{Token: "t", TenantID: 3}
	if _, err := ws.EnsureSynced(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	listsBefore := remote.listCalls
	readsBefore := remote.readCalls
	stats, err := ws.EnsureSynced(context.Background(), auth)
	if err != nil {
		t.Fatal(err)
	}
	if remote.listCalls != listsBefore {
		t.Fatalf("fresh reuse must not ListFiles again; lists=%d→%d", listsBefore, remote.listCalls)
	}
	if remote.readCalls != readsBefore {
		t.Fatalf("fresh reuse must not ReadFile; reads=%d→%d", readsBefore, remote.readCalls)
	}
	if stats.Fetched != 0 || stats.Skipped < 2 {
		t.Fatalf("stats=%+v", stats)
	}
}

type countingListStore struct {
	memStore
	listCalls int
	readCalls int
}

func (c *countingListStore) ListFiles(ctx context.Context, auth themefs.RequestAuth) ([]themefs.FileTreeEntry, error) {
	c.listCalls++
	return c.memStore.ListFiles(ctx, auth)
}

func (c *countingListStore) ReadFile(ctx context.Context, auth themefs.RequestAuth, relPath string) (string, error) {
	c.readCalls++
	return c.memStore.ReadFile(ctx, auth, relPath)
}

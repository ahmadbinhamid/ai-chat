package themeworkspace

import (
	"context"
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

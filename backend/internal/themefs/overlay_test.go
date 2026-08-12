package themefs

import (
	"context"
	"errors"
	"testing"
)

// TestOverlayStore_WriteFileReturnsError is item 11: applying a draft must
// always be an explicit, deliberate call (Service.ApplyDraft), never a side
// effect of code that thinks it's writing to a theme through an overlay.
func TestOverlayStore_WriteFileReturnsError(t *testing.T) {
	o := NewOverlayStore(nil, map[string]string{})
	if err := o.WriteFile(context.Background(), RequestAuth{}, "pages/home.liquid", "x", nil); !errors.Is(err, ErrOverlayIsReadOnly) {
		t.Fatalf("expected ErrOverlayIsReadOnly, got %v", err)
	}
}

func TestOverlayStore_DeleteFileReturnsError(t *testing.T) {
	o := NewOverlayStore(nil, map[string]string{})
	if err := o.DeleteFile(context.Background(), RequestAuth{}, "pages/home.liquid"); !errors.Is(err, ErrOverlayIsReadOnly) {
		t.Fatalf("expected ErrOverlayIsReadOnly, got %v", err)
	}
}

// fakeBaseStore is a minimal in-memory ThemeStore for overlay tests that
// don't need a real HTTP round trip — OverlayStore only ever calls base's
// exported ThemeStore methods, so a fake satisfying just those is enough.
type fakeBaseStore struct {
	tree  []FileTreeEntry
	files map[string]string
}

func (f *fakeBaseStore) ReadFile(_ context.Context, _ RequestAuth, relPath string) (string, error) {
	return f.files[relPath], nil
}
func (f *fakeBaseStore) WriteFile(context.Context, RequestAuth, string, string, *PageMeta) error {
	return errors.New("unexpected write to base store")
}
func (f *fakeBaseStore) DeleteFile(context.Context, RequestAuth, string) error {
	return errors.New("unexpected delete on base store")
}
func (f *fakeBaseStore) ListFiles(context.Context, RequestAuth) ([]FileTreeEntry, error) {
	return f.tree, nil
}
func (f *fakeBaseStore) CreateThemeFromBase(context.Context, RequestAuth) (string, error) {
	return "", nil
}

func TestOverlayStore_ReadFile_DraftHitBeatsBase(t *testing.T) {
	base := &fakeBaseStore{files: map[string]string{"pages/home.liquid": "SAVED"}}
	o := NewOverlayStore(base, map[string]string{"pages/home.liquid": "DRAFT"})

	got, err := o.ReadFile(context.Background(), RequestAuth{}, "pages/home.liquid")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if got != "DRAFT" {
		t.Fatalf("expected draft content to win, got %q", got)
	}
}

func TestOverlayStore_ReadFile_FallsThroughToBaseOnMiss(t *testing.T) {
	base := &fakeBaseStore{files: map[string]string{"pages/home.liquid": "SAVED"}}
	o := NewOverlayStore(base, map[string]string{"pages/other.liquid": "DRAFT"})

	got, err := o.ReadFile(context.Background(), RequestAuth{}, "pages/home.liquid")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if got != "SAVED" {
		t.Fatalf("expected base content on draft miss, got %q", got)
	}
}

// TestOverlayStore_ListFiles_MergesDraftOnlyPaths confirms a draft-created
// file (and its brand-new parent directory) shows up in ListFiles even
// though the base tree has never heard of it — required so
// list_theme_files/buildSnapshot's render-target-exists check can see it.
func TestOverlayStore_ListFiles_MergesDraftOnlyPaths(t *testing.T) {
	base := &fakeBaseStore{
		tree: []FileTreeEntry{
			{Name: "pages.json", Path: "pages.json", Type: "file"},
			{Name: "pages", Path: "pages", Type: "directory", Children: []FileTreeEntry{
				{Name: "home.liquid", Path: "pages/home.liquid", Type: "file"},
			}},
		},
	}
	o := NewOverlayStore(base, map[string]string{
		"pages/new.liquid":         "new page",
		"components/widget.liquid": "new component",
	})

	tree, err := o.ListFiles(context.Background(), RequestAuth{})
	if err != nil {
		t.Fatalf("ListFiles failed: %v", err)
	}

	found := make(map[string]bool)
	var walk func([]FileTreeEntry)
	walk = func(entries []FileTreeEntry) {
		for _, e := range entries {
			if e.Type == "file" {
				found[e.Path] = true
			}
			walk(e.Children)
		}
	}
	walk(tree)

	for _, want := range []string{"pages.json", "pages/home.liquid", "pages/new.liquid", "components/widget.liquid"} {
		if !found[want] {
			t.Errorf("expected merged tree to contain %q, got %+v", want, found)
		}
	}
}

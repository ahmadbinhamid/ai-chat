package themefs

import (
	"context"
	"errors"
	"strings"
)

// ErrOverlayIsReadOnly is what WriteFile/DeleteFile return on an
// OverlayStore — see its own doc comment for why applying a draft must
// always be an explicit, deliberate call (Service.ApplyDraft) and never a
// side effect of code that thinks it's writing to a theme.
var ErrOverlayIsReadOnly = errors.New("draft overlay is read-only — apply the draft to write it to the theme")

// OverlayStore serves reads from an in-memory draft first, falling through
// to the real store for anything the draft doesn't cover — this is what
// lets a whole chat's worth of turns build on each other (read their own
// and each other's unsaved edits) before any of them ever reaches FlowPOS.
// Read-only by contract: WriteFile and DeleteFile always return
// ErrOverlayIsReadOnly. Nothing in the generation path is meant to write
// through this type — the only writer of a theme is Service.ApplyDraft,
// which deliberately uses the base store directly, never an overlay (see
// its own doc comment) — so a WriteFile call reaching this type at all
// means something upstream has a bug, and failing loudly here catches it
// immediately instead of silently doing nothing or, worse, silently
// updating the draft map in a way nothing else observes.
type OverlayStore struct {
	base  ThemeStore
	draft map[string]string
}

// NewOverlayStore wraps base with draft — draft is read, never mutated (an
// OverlayStore never writes to it, matching WriteFile/DeleteFile's
// contract above).
func NewOverlayStore(base ThemeStore, draft map[string]string) *OverlayStore {
	return &OverlayStore{base: base, draft: draft}
}

// ReadFile returns the draft's content for relPath if the draft has an
// entry for it — including an explicitly empty one, since a draft entry
// existing at all means some earlier turn in this chat wrote it, and ""
// there is a real deleted-to-empty content, not "no draft entry" (Go's map
// lookup ", ok" is what disambiguates the two, not a truthiness check on
// the string). Falls through to base otherwise.
func (o *OverlayStore) ReadFile(ctx context.Context, auth RequestAuth, relPath string) (string, error) {
	if content, ok := o.draft[relPath]; ok {
		return content, nil
	}
	return o.base.ReadFile(ctx, auth, relPath)
}

func (o *OverlayStore) WriteFile(context.Context, RequestAuth, string, string, *PageMeta) error {
	return ErrOverlayIsReadOnly
}

func (o *OverlayStore) DeleteFile(context.Context, RequestAuth, string) error {
	return ErrOverlayIsReadOnly
}

// ListFiles delegates to base, then merges in any draft path the real
// tree doesn't have yet — a draft-created file has to be visible here or
// list_theme_files/buildSnapshot's render-target-exists check can't see it
// exists. Directories are synthesized to match the same shape the real
// endpoint returns (a directory carries Children, a file doesn't) so a
// caller walking the merged tree can't tell a synthesized node from a real
// one.
func (o *OverlayStore) ListFiles(ctx context.Context, auth RequestAuth) ([]FileTreeEntry, error) {
	tree, err := o.base.ListFiles(ctx, auth)
	if err != nil {
		return nil, err
	}
	if len(o.draft) == 0 {
		return tree, nil
	}

	existing := make(map[string]bool)
	collectPaths(tree, existing)

	root := treeToDirNode(tree)
	for path := range o.draft {
		if existing[path] {
			continue
		}
		insertPath(root, strings.Split(path, "/"))
	}
	return root.toEntries(), nil
}

func (o *OverlayStore) CreateThemeFromBase(ctx context.Context, auth RequestAuth) (string, error) {
	return o.base.CreateThemeFromBase(ctx, auth)
}

func collectPaths(entries []FileTreeEntry, into map[string]bool) {
	for _, e := range entries {
		if e.Type == "file" {
			into[e.Path] = true
		}
		if len(e.Children) > 0 {
			collectPaths(e.Children, into)
		}
	}
}

// dirNode is a mutable tree used only while merging draft paths into the
// real tree — converted to/from []FileTreeEntry at the boundary so the
// public shape (FileTreeEntry) never needs a parent pointer or a
// name->child map of its own.
type dirNode struct {
	name     string
	path     string
	isFile   bool
	children map[string]*dirNode
	// order preserves first-seen order across children so output is
	// deterministic given the same input, not map-iteration-order flaky.
	order []string
}

func newDirNode(name, path string) *dirNode {
	return &dirNode{name: name, path: path, children: make(map[string]*dirNode)}
}

// Children converts this node's subtree back into []FileTreeEntry —
// directories always carry Children (possibly empty-but-non-nil is never
// produced; a dir with no entries never exists here), files never do,
// matching FileTreeEntry's own doc comment on the real endpoint's shape.
func (d *dirNode) toEntries() []FileTreeEntry {
	entries := make([]FileTreeEntry, 0, len(d.order))
	for _, name := range d.order {
		child := d.children[name]
		if child.isFile {
			entries = append(entries, FileTreeEntry{Name: child.name, Path: child.path, Type: "file"})
			continue
		}
		entries = append(entries, FileTreeEntry{
			Name: child.name, Path: child.path, Type: "directory", Children: child.toEntries(),
		})
	}
	return entries
}

// treeToDirNode converts the real tree (as returned by base.ListFiles) into
// a mutable dirNode so draft-only paths can be inserted into it uniformly,
// regardless of whether their parent directory already exists in the real
// tree or has to be synthesized too.
func treeToDirNode(entries []FileTreeEntry) *dirNode {
	root := newDirNode("", "")
	var walk func(node *dirNode, entries []FileTreeEntry)
	walk = func(node *dirNode, entries []FileTreeEntry) {
		for _, e := range entries {
			child := newDirNode(e.Name, e.Path)
			child.isFile = e.Type == "file"
			node.children[e.Name] = child
			node.order = append(node.order, e.Name)
			if e.Type == "directory" {
				walk(child, e.Children)
			}
		}
	}
	walk(root, entries)
	return root
}

// insertPath walks/creates directory nodes for every segment of a draft
// path except the last, then adds the last segment as a file node — used
// to graft a draft-only path (which may need one or more new synthetic
// directories, e.g. a draft's first file in a brand-new components/
// subfolder) into the tree being built for ListFiles.
func insertPath(root *dirNode, segments []string) {
	node := root
	for i, seg := range segments {
		last := i == len(segments)-1
		child, ok := node.children[seg]
		if !ok {
			segPath := seg
			if node.path != "" {
				segPath = node.path + "/" + seg
			}
			child = newDirNode(seg, segPath)
			child.isFile = last
			node.children[seg] = child
			node.order = append(node.order, seg)
		}
		node = child
	}
}

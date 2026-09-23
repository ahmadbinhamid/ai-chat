package themefs

import (
	"context"
	"errors"
	"strings"
)

// ErrOverlayIsReadOnly is what WriteFile/DeleteFile return — applying a draft must be an explicit call (Service.ApplyDraft), never a side effect.
var ErrOverlayIsReadOnly = errors.New("draft overlay is read-only — apply the draft to write it to the theme")

// OverlayStore serves reads from an in-memory draft first, falling through to the real store — lets a chat's turns build
// on each other's unsaved edits before reaching FlowPOS. Read-only by contract; a write reaching this type means an upstream bug, caught loudly rather than silently no-opped.
type OverlayStore struct {
	base  ThemeStore
	draft map[string]string
}

// NewOverlayStore wraps base with draft — draft is read, never mutated, matching WriteFile/DeleteFile's read-only contract.
func NewOverlayStore(base ThemeStore, draft map[string]string) *OverlayStore {
	return &OverlayStore{base: base, draft: draft}
}

// ReadFile returns the draft's content for relPath if present, including an explicitly empty one — disambiguated via
// map ", ok", not a truthiness check, since "" can be real deleted-to-empty content, not "no draft entry". Falls through to base otherwise.
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

// ListFiles delegates to base, then merges in any draft path the real tree doesn't have yet — needed so
// list_theme_files/buildSnapshot's render-target-exists check can see a draft-created file. Synthesized directories match the real endpoint's shape exactly.
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

// dirNode is a mutable tree used only while merging draft paths into the real tree, converted to/from []FileTreeEntry at the boundary.
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

// toEntries converts this node's subtree back into []FileTreeEntry, matching the real endpoint's shape (directories carry Children, files don't).
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

// treeToDirNode converts the real tree into a mutable dirNode so draft-only paths can be inserted uniformly, whether or not their parent already exists.
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

// insertPath walks/creates directory nodes for a draft path's segments, adding the last as a file node — grafts a
// draft-only path (with synthetic parent dirs if needed) into the tree.
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

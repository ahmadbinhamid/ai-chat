package themefs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ComponentInfo describes one components/*.liquid or liquid/partials/*.liquid
// file's inferred call signature — this dialect has no formal parameter
// declaration syntax (§1: render passes only explicit params, but a
// component never declares what it expects), so Params is a heuristic:
// every bare identifier the component's own markup references that isn't
// one of §7's global context objects and isn't a name the component itself
// defines (an assign/capture target or a for-loop variable) is assumed to
// be an incoming param — which is exactly the same "unknown root = a
// render param, not a data-model object" assumption themecheck's
// known-fields rule already makes about component markup.
type ComponentInfo struct {
	Path   string
	Params []string
}

// Manifest is a snapshot of the active theme's structure: every file path,
// plus a component/partial param-signature index — given to the model as
// grounding (see ai.ThemeContext.Manifest) so it knows what params an
// existing component actually expects instead of guessing.
type Manifest struct {
	ContentHash string
	GeneratedAt time.Time
	Files       []string
	Components  []ComponentInfo
}

// globalContextRoots are §7's context object names — never an inferred
// component param, since these are populated globally by the platform, not
// passed in by whatever renders the component.
var globalContextRoots = map[string]bool{
	"page": true, "store": true, "theme": true, "menu": true, "path": true,
	"customer": true, "customer_authenticated": true, "auth_check": true,
	"environment": true, "csrf_token": true, "product": true, "products": true,
	"category": true, "categories": true, "filter_categories": true,
	"filters": true, "filter_price_range": true, "basket": true, "forloop": true,
}

var (
	manifestOutputRootRe = regexp.MustCompile(`\{\{-?\s*([a-zA-Z_][a-zA-Z0-9_]*)`)
	manifestCondRootRe   = regexp.MustCompile(`\{%-?\s*(?:if|elsif)\s+([a-zA-Z_][a-zA-Z0-9_]*)`)
	manifestForSourceRe  = regexp.MustCompile(`\{%-?\s*for\s+\w+\s+in\s+([a-zA-Z_][a-zA-Z0-9_]*)`)
	manifestForVarRe     = regexp.MustCompile(`\{%-?\s*for\s+(\w+)\s+in\b`)
	manifestAssignRe     = regexp.MustCompile(`\{%-?\s*assign\s+(\w+)\s*=`)
	manifestCaptureRe    = regexp.MustCompile(`\{%-?\s*capture\s+(\w+)`)
)

// inferParams applies the heuristic described on ComponentInfo to one
// component's source, returning its inferred params sorted for a stable,
// diffable manifest.
func inferParams(content string) []string {
	defined := map[string]bool{}
	for _, m := range manifestForVarRe.FindAllStringSubmatch(content, -1) {
		defined[m[1]] = true
	}
	for _, m := range manifestAssignRe.FindAllStringSubmatch(content, -1) {
		defined[m[1]] = true
	}
	for _, m := range manifestCaptureRe.FindAllStringSubmatch(content, -1) {
		defined[m[1]] = true
	}

	referenced := map[string]bool{}
	for _, re := range []*regexp.Regexp{manifestOutputRootRe, manifestCondRootRe, manifestForSourceRe} {
		for _, m := range re.FindAllStringSubmatch(content, -1) {
			referenced[m[1]] = true
		}
	}

	var params []string
	for name := range referenced {
		if defined[name] || globalContextRoots[name] {
			continue
		}
		params = append(params, name)
	}
	sort.Strings(params)
	return params
}

// isComponentOrPartial reports whether path is the kind of file
// GenerateManifest indexes params for — components and liquid/partials
// only (§8): pages are entry points, not things another file renders with
// explicit params to introspect.
func isComponentOrPartial(path string) bool {
	return strings.HasSuffix(path, ".liquid") &&
		(strings.HasPrefix(path, "components/") || strings.HasPrefix(path, "liquid/partials/"))
}

// GenerateManifest fetches every file in the active theme and builds a
// fresh Manifest — always does the full work (list + read every .liquid
// file + infer every component's params); see GetOrGenerateManifest for
// the cached version most callers want instead.
func (s *Store) GenerateManifest(ctx context.Context, auth RequestAuth) (Manifest, error) {
	tree, err := s.ListFiles(ctx, auth)
	if err != nil {
		return Manifest{}, err
	}
	paths := map[string]bool{}
	flattenTree(tree, paths)

	files := make([]string, 0, len(paths))
	for p := range paths {
		files = append(files, p)
	}
	sort.Strings(files)

	hasher := sha256.New()
	var components []ComponentInfo
	for _, p := range files {
		if !strings.HasSuffix(p, ".liquid") {
			hasher.Write([]byte(p + "\n"))
			continue
		}
		content, err := s.ReadFile(ctx, auth, p)
		if err != nil {
			return Manifest{}, err
		}
		hasher.Write([]byte(p + ":" + content + "\n"))
		if isComponentOrPartial(p) {
			components = append(components, ComponentInfo{Path: p, Params: inferParams(content)})
		}
	}
	sort.Slice(components, func(i, j int) bool { return components[i].Path < components[j].Path })

	return Manifest{
		ContentHash: hex.EncodeToString(hasher.Sum(nil)),
		GeneratedAt: time.Now().UTC(),
		Files:       files,
		Components:  components,
	}, nil
}

// flattenTree records every FILE path (not directories) from a theme's file
// tree into paths — a private copy of the same walk themebuild's
// flattenFileTree does, kept local to this package rather than shared,
// since themefs must not depend on themebuild (dependencies only ever flow
// the other way — see themebuild's own doc comment).
func flattenTree(entries []FileTreeEntry, paths map[string]bool) {
	for _, e := range entries {
		if e.Type == "file" {
			paths[e.Path] = true
		}
		if len(e.Children) > 0 {
			flattenTree(e.Children, paths)
		}
	}
}

// manifestCacheEntry pairs a cached Manifest with the cheap tree
// fingerprint (see treeFingerprint) it was built from.
type manifestCacheEntry struct {
	fingerprint string
	manifest    Manifest
}

// manifestCache is Store's process-local cache of the last manifest built
// per tenant.
type manifestCache struct {
	mu      sync.Mutex
	entries map[uint64]manifestCacheEntry
}

// treeFingerprint hashes just the file tree's paths+types (one ListFiles
// call, no file content) — a cheap-to-compute signal for "did the set of
// files change" that GetOrGenerateManifest checks before paying for a full
// GenerateManifest (which reads every .liquid file's content to build the
// component param index and the manifest's own ContentHash). This misses a
// same-paths-different-content edit — a real trade-off, not an oversight:
// themefs has no local disk to stat an mtime from and no cheaper signal
// this HTTP API exposes (see the package doc comment on why), so "the file
// set is unchanged" is the best cheap proxy available for "probably still
// the same manifest".
func treeFingerprint(entries []FileTreeEntry) string {
	paths := map[string]bool{}
	flattenTree(entries, paths)
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)
	h := sha256.New()
	for _, p := range sorted {
		h.Write([]byte(p + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// GetOrGenerateManifest returns auth.TenantID's cached manifest if the
// theme's file tree hasn't changed since it was last computed (a single,
// cheap ListFiles call to check), or generates — and caches — a fresh one
// otherwise. Use this instead of GenerateManifest directly unless a
// guaranteed-fresh manifest matters more than avoiding the extra work.
func (s *Store) GetOrGenerateManifest(ctx context.Context, auth RequestAuth) (Manifest, error) {
	tree, err := s.ListFiles(ctx, auth)
	if err != nil {
		return Manifest{}, err
	}
	fingerprint := treeFingerprint(tree)

	s.manifests.mu.Lock()
	cached, ok := s.manifests.entries[auth.TenantID]
	s.manifests.mu.Unlock()
	if ok && cached.fingerprint == fingerprint {
		return cached.manifest, nil
	}

	fresh, err := s.GenerateManifest(ctx, auth)
	if err != nil {
		return Manifest{}, err
	}

	s.manifests.mu.Lock()
	if s.manifests.entries == nil {
		s.manifests.entries = make(map[uint64]manifestCacheEntry)
	}
	s.manifests.entries[auth.TenantID] = manifestCacheEntry{fingerprint: fingerprint, manifest: fresh}
	s.manifests.mu.Unlock()
	return fresh, nil
}

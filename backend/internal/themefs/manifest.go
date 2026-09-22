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

// ComponentInfo describes a component/partial's inferred call signature. This dialect has no param declaration syntax,
// so Params is a heuristic: any bare identifier not a §7 global and not self-defined (assign/capture/for-loop var) is assumed to be a param.
type ComponentInfo struct {
	Path   string
	Params []string
}

// Manifest is a snapshot of the theme's structure — every file path plus a component param-signature index, given to the model as grounding.
type Manifest struct {
	ContentHash string
	GeneratedAt time.Time
	Files       []string
	Components  []ComponentInfo
}

// globalContextRoots are §7's context object names — never inferred as a component param since the platform populates them globally.
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

// inferParams applies ComponentInfo's heuristic to one component's source, returning params sorted for a stable, diffable manifest.
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

// isComponentOrPartial reports whether path is a components/ or liquid/partials/ file (§8) — pages are entry points, not things introspected for params.
func isComponentOrPartial(path string) bool {
	return strings.HasSuffix(path, ".liquid") &&
		(strings.HasPrefix(path, "components/") || strings.HasPrefix(path, "liquid/partials/"))
}

// GenerateManifest fetches every theme file and builds a fresh Manifest, always doing the full work — see GetOrGenerateManifest for the cached version most callers want.
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

// flattenTree records every file path (not directories) from the tree into paths — a local copy of themebuild's
// flattenFileTree, kept separate since themefs must not depend on themebuild.
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

// treeFingerprint hashes just the tree's paths+types — a cheap signal GetOrGenerateManifest checks before paying for a
// full GenerateManifest. Known trade-off: misses a same-paths-different-content edit; there's no cheaper signal available.
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

// GetOrGenerateManifest returns auth.TenantID's cached manifest if the file tree hasn't changed (one cheap ListFiles
// check), else generates and caches a fresh one. Prefer this over GenerateManifest unless a guaranteed-fresh manifest matters more.
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
